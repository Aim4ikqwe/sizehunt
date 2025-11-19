package hash

import "golang.org/x/crypto/bcrypt"

func HashPassword(p string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(p), bcrypt.DefaultCost)
	return string(bytes), err
}

func CheckPassword(hashed, plain string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hashed), []byte(plain))
	return err == nil
}
