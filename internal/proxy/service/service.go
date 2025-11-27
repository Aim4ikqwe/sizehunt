package service

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"
	"github.com/pkg/errors"

	"sizehunt/internal/proxy"
	"sizehunt/internal/proxy/repository"
)

type ProxyService struct {
	Repo      repository.ProxyRepository
	instances map[int64]*proxy.ProxyInstance
	mu        sync.Mutex
	dockerCli *client.Client
}

func NewProxyService(repo repository.ProxyRepository) *ProxyService {
	// Инициализация Docker клиента
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		log.Fatalf("Failed to create Docker client: %v", err)
	}

	return &ProxyService{
		Repo:      repo,
		instances: make(map[int64]*proxy.ProxyInstance),
		dockerCli: cli,
	}
}

func (s *ProxyService) ConfigureProxy(ctx context.Context, userID int64, ssAddr, ssMethod, ssPassword string) error {
	// Сохраняем конфиг
	_, err := s.Repo.SaveProxyConfig(ctx, userID, ssAddr, ssMethod, ssPassword)
	if err != nil {
		return errors.Wrap(err, "failed to save proxy config")
	}
	// Запускаем прокси
	return s.StartProxyForUser(ctx, userID)
}

func (s *ProxyService) StartProxyForUser(ctx context.Context, userID int64) error {
	config, err := s.Repo.GetProxyConfig(ctx, userID)
	if err != nil {
		return errors.Wrap(err, "failed to get proxy config")
	}
	if config.Status == "running" {
		return nil // уже запущен
	}

	// Проверяем, существует ли уже контейнер для этого пользователя
	containerName := fmt.Sprintf("ss-proxy-%d", userID)
	_, err = s.dockerCli.ContainerInspect(ctx, containerName)
	if err == nil {
		if err := s.dockerCli.ContainerStop(ctx, containerName, container.StopOptions{}); err != nil {
			log.Printf("Failed to stop existing container: %v", err)
		}
		if err := s.dockerCli.ContainerRemove(ctx, containerName, container.RemoveOptions{Force: true}); err != nil {
			log.Printf("Failed to remove existing container: %v", err)
		}
	}

	// Создаем новый контейнер
	containerPort := nat.Port("1080/tcp")
	portBinding := nat.PortBinding{
		HostIP:   "127.0.0.1",
		HostPort: fmt.Sprintf("%d", config.LocalPort),
	}
	portMap := nat.PortMap{}
	portMap[containerPort] = []nat.PortBinding{portBinding}

	exposedPorts := nat.PortSet{}
	exposedPorts[nat.Port(containerPort)] = struct{}{}
	resp, err := s.dockerCli.ContainerCreate(ctx, &container.Config{
		Image: "shadowsocks/shadowsocks-libev:edge",
		Cmd: []string{
			"ss-local",
			"-s", config.SSAddr,
			"-p", "8388",
			"-m", config.SSMethod,
			"-k", config.SSPassword,
			"-b", "0.0.0.0",
			"-l", fmt.Sprintf("%d", 1080),
			"-u",
			"--fast-open",
		},
		ExposedPorts: exposedPorts,
	}, &container.HostConfig{
		PortBindings: portMap,
		AutoRemove:   false,
	}, nil, nil, containerName)

	if err != nil {
		s.Repo.UpdateStatus(ctx, config.ID, "error")
		return errors.Wrap(err, "failed to create container")
	}

	// Запускаем контейнер
	if err := s.dockerCli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		s.Repo.UpdateStatus(ctx, config.ID, "error")
		return errors.Wrap(err, "failed to start container")
	}

	// Ждем, пока контейнер запустится и станет готовым к работе
	for i := 0; i < 10; i++ {
		containerJSON, err := s.dockerCli.ContainerInspect(ctx, containerName)
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		if containerJSON.State.Running {
			// Контейнер запущен, проверяем логи на наличие ошибок
			logsOptions := container.LogsOptions{
				ShowStdout: true,
				ShowStderr: true,
				Tail:       "10",
			}
			logs, err := s.dockerCli.ContainerLogs(ctx, containerName, logsOptions)
			if err == nil {
				buf := new(strings.Builder)
				_, err = stdcopy.StdCopy(buf, buf, logs)
				if err == nil {
					if strings.Contains(strings.ToLower(buf.String()), "error") || strings.Contains(strings.ToLower(buf.String()), "fail") {
						log.Printf("Container logs for %s: %s", containerName, buf.String())
						s.dockerCli.ContainerStop(ctx, containerName, container.StopOptions{})
						s.Repo.UpdateStatus(ctx, config.ID, "error")
						return fmt.Errorf("container started with errors")
					}
				}
				logs.Close()
			}
			break
		}

		time.Sleep(500 * time.Millisecond)
	}

	s.mu.Lock()
	s.instances[userID] = &proxy.ProxyInstance{
		Config:      config,
		ContainerID: containerName,
		Status:      "running",
	}
	s.mu.Unlock()

	s.Repo.UpdateStatus(ctx, config.ID, "running")

	// Запускаем горутину для мониторинга состояния контейнера
	go s.monitorContainer(ctx, userID, containerName)

	return nil
}

func (s *ProxyService) monitorContainer(ctx context.Context, userID int64, containerName string) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			containerJSON, err := s.dockerCli.ContainerInspect(ctx, containerName)
			if err != nil {
				// Контейнер не существует, возможно, был удален
				s.mu.Lock()
				delete(s.instances, userID)
				s.mu.Unlock()
				if instance, ok := s.instances[userID]; ok {
					s.Repo.UpdateStatus(ctx, instance.Config.ID, "stopped")
				}
				return
			}

			if !containerJSON.State.Running {
				// Контейнер упал, пытаемся перезапустить
				log.Printf("Container %s for user %d is not running, attempting restart", containerName, userID)
				if err := s.dockerCli.ContainerStart(ctx, containerName, container.StartOptions{}); err != nil {
					log.Printf("Failed to restart container %s: %v", containerName, err)

					s.mu.Lock()
					delete(s.instances, userID)
					s.mu.Unlock()

					if instance, ok := s.instances[userID]; ok {
						s.Repo.UpdateStatus(ctx, instance.Config.ID, "error")
					}
					return
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

func (s *ProxyService) StopProxyForUser(ctx context.Context, userID int64) error {
	s.mu.Lock()
	instance, exists := s.instances[userID]
	s.mu.Unlock()

	if !exists || instance == nil {
		return nil
	}

	if instance.ContainerID != "" {
		// Останавливаем контейнер
		if err := s.dockerCli.ContainerStop(ctx, instance.ContainerID, container.StopOptions{}); err != nil {
			// Если не удалось остановить, пытаемся удалить принудительно
			if err := s.dockerCli.ContainerRemove(ctx, instance.ContainerID, container.RemoveOptions{
				Force: true,
			}); err != nil {
				log.Printf("Failed to remove container %s: %v", instance.ContainerID, err)
			}
		}
	}

	if instance.Config != nil {
		s.Repo.UpdateStatus(ctx, instance.Config.ID, "stopped")
	}

	s.mu.Lock()
	delete(s.instances, userID)
	s.mu.Unlock()

	return nil
}

func (s *ProxyService) GetProxyAddressForUser(userID int64) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	instance, exists := s.instances[userID]
	if !exists || instance == nil || instance.Status != "running" {
		return "", false
	}

	// Проверяем, что контейнер действительно запущен
	ctx := context.Background()
	containerJSON, err := s.dockerCli.ContainerInspect(ctx, instance.ContainerID)
	if err != nil || !containerJSON.State.Running {
		return "", false
	}

	return fmt.Sprintf("127.0.0.1:%d", instance.Config.LocalPort), true
}

func (s *ProxyService) StopAllProxies(ctx context.Context) {
	s.mu.Lock()
	userIDs := make([]int64, 0, len(s.instances))
	for userID := range s.instances {
		userIDs = append(userIDs, userID)
	}
	s.mu.Unlock()

	for _, userID := range userIDs {
		if err := s.StopProxyForUser(ctx, userID); err != nil {
			log.Printf("Failed to stop proxy for user %d: %v", userID, err)
		}
	}
}

func (s *ProxyService) DeleteProxyConfig(ctx context.Context, userID int64) error {
	// Сначала останавливаем прокси
	if err := s.StopProxyForUser(ctx, userID); err != nil {
		return errors.Wrap(err, "failed to stop proxy")
	}

	// Удаляем конфиг из БД
	if err := s.Repo.DeleteProxyConfig(ctx, userID); err != nil {
		return errors.Wrap(err, "failed to delete proxy config from database")
	}

	return nil
}
