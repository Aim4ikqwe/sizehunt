package service

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"sizehunt/internal/proxy"
	"sizehunt/internal/proxy/repository"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"
	"github.com/pkg/errors"
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

func (s *ProxyService) ConfigureProxy(ctx context.Context, userID int64, ssAddr string, ssPort int, ssMethod, ssPassword string) error {
	// Сохраняем/обновляем конфиг с портом
	localPort, err := s.Repo.SaveProxyConfig(ctx, userID, ssAddr, ssPort, ssMethod, ssPassword)
	if err != nil {
		return errors.Wrap(err, "failed to save proxy config")
	}
	log.Printf("Proxy config saved for user %d with local port %d", userID, localPort)

	// Если у пользователя есть активные сигналы, запускаем прокси
	hasActiveSignals, err := s.hasActiveSignalsForUser(ctx, userID)
	if err != nil {
		log.Printf("Failed to check active signals for user %d: %v", userID, err)
	} else if hasActiveSignals {
		// Запускаем прокси
		return s.StartProxyForUser(ctx, userID)
	}

	return nil
}

func (s *ProxyService) hasActiveSignalsForUser(ctx context.Context, userID int64) (bool, error) {
	// Этот метод нужно будет реализовать в другом месте, пока заглушка
	// В реальном приложении здесь будет запрос к базе данных
	return true, nil
}

func (s *ProxyService) StartProxyForUser(ctx context.Context, userID int64) error {
	config, err := s.Repo.GetProxyConfig(ctx, userID)
	if err != nil {
		return errors.Wrap(err, "failed to get proxy config")
	}

	// Определяем имя контейнера в самом начале
	containerName := fmt.Sprintf("ss-proxy-%d", userID)

	// Проверяем текущий статус прокси
	if config.Status == "running" {
		// Проверяем, существует ли контейнер и запущен ли он
		_, err := s.dockerCli.ContainerInspect(ctx, containerName)
		if err == nil {
			containerJSON, err := s.dockerCli.ContainerInspect(ctx, containerName)
			if err == nil && containerJSON.State.Running {
				log.Printf("Proxy container already running for user %d", userID)
				s.mu.Lock()
				s.instances[userID] = &proxy.ProxyInstance{
					Config:      config,
					ContainerID: containerName,
					Status:      "running",
				}
				s.mu.Unlock()
				return nil // Контейнер уже запущен
			}
		}
		// Если контейнер не существует или не запущен, но статус в БД "running",
		// обновляем статус на "stopped" перед запуском нового контейнера
		if err := s.Repo.UpdateStatus(ctx, config.ID, "stopped"); err != nil {
			log.Printf("Failed to update proxy status for user %d: %v", userID, err)
		}
	}

	// Проверяем, существует ли уже контейнер для этого пользователя и удаляем его при необходимости
	_, err = s.dockerCli.ContainerInspect(ctx, containerName)
	if err == nil {
		log.Printf("Stopping existing container %s for user %d", containerName, userID)
		if err := s.dockerCli.ContainerStop(ctx, containerName, container.StopOptions{}); err != nil {
			log.Printf("Failed to stop existing container: %v", err)
		}
		log.Printf("Removing existing container %s for user %d", containerName, userID)
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
			"-p", fmt.Sprintf("%d", config.SSPort),
			"-m", config.SSMethod,
			"-k", config.SSPassword,
			"-b", "0.0.0.0",
			"-l", "1080", // Всегда 1080 внутри контейнера
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
					logOutput := buf.String()
					if strings.Contains(strings.ToLower(logOutput), "error") ||
						strings.Contains(strings.ToLower(logOutput), "fail") ||
						strings.Contains(strings.ToLower(logOutput), "invalid") {
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
	log.Printf("Starting monitor for container %s (user %d)", containerName, userID)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			containerJSON, err := s.dockerCli.ContainerInspect(ctx, containerName)
			if err != nil {
				// Контейнер не существует, возможно, был удален
				log.Printf("Container %s for user %d does not exist anymore", containerName, userID)
				s.mu.Lock()
				delete(s.instances, userID)
				s.mu.Unlock()
				if instance, ok := s.instances[userID]; ok && instance.Config != nil {
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
					if instance, ok := s.instances[userID]; ok && instance.Config != nil {
						s.Repo.UpdateStatus(ctx, instance.Config.ID, "error")
					}
					return
				} else {
					log.Printf("Container %s for user %d successfully restarted", containerName, userID)
				}
			}
		case <-ctx.Done():
			log.Printf("Stopping monitor for container %s (user %d)", containerName, userID)
			return
		}
	}
}

func (s *ProxyService) StopProxyForUser(ctx context.Context, userID int64) error {
	log.Printf("ProxyService: Stopping proxy for user %d", userID)
	s.mu.Lock()
	instance, exists := s.instances[userID]
	if !exists || instance == nil {
		s.mu.Unlock()
		log.Printf("ProxyService: No proxy instance found for user %d", userID)
		return nil
	}
	containerID := instance.ContainerID
	config := instance.Config
	s.mu.Unlock()

	if containerID == "" {
		log.Printf("ProxyService: No container ID for user %d", userID)
		return nil
	}

	log.Printf("ProxyService: Stopping container %s for user %d", containerID, userID)
	stopCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	// Пытаемся корректно остановить контейнер
	err := s.dockerCli.ContainerStop(stopCtx, containerID, container.StopOptions{})
	if err != nil {
		log.Printf("ProxyService: Failed to stop container %s: %v, attempting force removal", containerID, err)
		removeCtx, removeCancel := context.WithTimeout(ctx, 5*time.Second)
		defer removeCancel()
		if err := s.dockerCli.ContainerRemove(removeCtx, containerID, container.RemoveOptions{
			Force: true,
		}); err != nil {
			log.Printf("ProxyService: Failed to remove container %s: %v", containerID, err)
			return err
		}
	} else {
		log.Printf("ProxyService: Container %s stopped successfully", containerID)
		// Удаляем контейнер после остановки
		removeCtx, removeCancel := context.WithTimeout(ctx, 5*time.Second)
		defer removeCancel()
		if err := s.dockerCli.ContainerRemove(removeCtx, containerID, container.RemoveOptions{}); err != nil {
			log.Printf("ProxyService: Failed to remove container %s after stop: %v", containerID, err)
		}
	}

	if config != nil {
		log.Printf("ProxyService: Updating proxy status to 'stopped' for user %d", userID)
		if err := s.Repo.UpdateStatus(ctx, config.ID, "stopped"); err != nil {
			log.Printf("ProxyService: Failed to update proxy status for user %d: %v", userID, err)
		}
	}

	s.mu.Lock()
	delete(s.instances, userID)
	s.mu.Unlock()
	log.Printf("ProxyService: Proxy container fully stopped and removed for user %d", userID)
	return nil
}

func (s *ProxyService) StopAllProxies(ctx context.Context) {
	log.Println("Stopping all proxy containers...")
	s.mu.Lock()
	userIDs := make([]int64, 0, len(s.instances))
	for userID := range s.instances {
		userIDs = append(userIDs, userID)
	}
	s.mu.Unlock()
	for _, userID := range userIDs {
		log.Printf("Stopping proxy container for user %d", userID)
		if err := s.StopProxyForUser(ctx, userID); err != nil {
			log.Printf("Failed to stop proxy for user %d: %v", userID, err)
		} else {
			log.Printf("Successfully stopped proxy for user %d", userID)
		}
	}
	log.Println("All proxy containers stopped")
}

func (s *ProxyService) DeleteProxyConfig(ctx context.Context, userID int64) error {
	log.Printf("Deleting proxy config for user %d", userID)
	// Сначала останавливаем прокси
	if err := s.StopProxyForUser(ctx, userID); err != nil {
		log.Printf("Failed to stop proxy for user %d during deletion: %v", userID, err)
		// Не возвращаем ошибку, продолжаем удаление конфигурации
	}
	// Удаляем конфиг из БД
	if err := s.Repo.DeleteProxyConfig(ctx, userID); err != nil {
		return errors.Wrap(err, "failed to delete proxy config from database")
	}
	log.Printf("Proxy config successfully deleted for user %d", userID)
	return nil
}

// IsProxyRunningForUser проверяет, запущен ли прокси для пользователя
func (s *ProxyService) IsProxyRunningForUser(userID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	instance, exists := s.instances[userID]
	if !exists || instance == nil {
		return false
	}
	return instance.Status == "running"
}
func (s *ProxyService) GetProxyAddressForUser(userID int64) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	instance, exists := s.instances[userID]
	if !exists || instance == nil || instance.Status != "running" {
		return "", false
	}
	// Проверяем, что контейнер действительно запущен
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	containerJSON, err := s.dockerCli.ContainerInspect(ctx, instance.ContainerID)
	if err != nil || !containerJSON.State.Running {
		return "", false
	}
	return fmt.Sprintf("127.0.0.1:%d", instance.Config.LocalPort), true
}

// HasProxyConfig проверяет, есть ли у пользователя сохраненные настройки прокси
func (s *ProxyService) HasProxyConfig(userID int64) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	config, err := s.Repo.GetProxyConfig(ctx, userID)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	return config != nil, nil
}

// GetProxyConfig возвращает конфигурацию прокси для пользователя
func (s *ProxyService) GetProxyConfig(ctx context.Context, userID int64) (*proxy.ProxyConfig, error) {
	return s.Repo.GetProxyConfig(ctx, userID)
}
