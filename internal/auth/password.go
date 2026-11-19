package auth

import (
	"errors"
	"regexp"

	"golang.org/x/crypto/bcrypt"
)

// HashPassword hashes a plain password using bcrypt.
func HashPassword(plain string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// ComparePassword compares a bcrypt hash with a plain password.
func ComparePassword(hash, plain string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain))
}

// ValidatePasswordStrength checks if password meets minimum strength requirements:
// - At least 8 characters
// - Contains at least one letter
// - Contains at least one digit
func ValidatePasswordStrength(password string) error {
	if len(password) < 8 {
		return errors.New("password must be at least 8 characters")
	}

	hasLetter := regexp.MustCompile(`[a-zA-Z]`).MatchString(password)
	hasDigit := regexp.MustCompile(`[0-9]`).MatchString(password)

	if !hasLetter {
		return errors.New("password must contain at least one letter")
	}
	if !hasDigit {
		return errors.New("password must contain at least one digit")
	}

	return nil
}
