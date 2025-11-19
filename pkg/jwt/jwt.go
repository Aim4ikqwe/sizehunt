package jwt

import "github.com/golang-jwt/jwt/v5"

func GenerateToken(secret, email string) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"email": email,
	})
	return token.SignedString([]byte(secret))
}
