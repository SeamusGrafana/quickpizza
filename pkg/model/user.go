package model

import (
	"errors"
	"math/rand"

	"github.com/uptrace/bun"
)

const (
	UserTokenLength = 16
	MaxNameLength   = 32
)

var characters = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")

func GenerateUserToken() string {
	data := make([]rune, UserTokenLength)
	for i := range data {
		// NOTE: This should use a cryptographically-safe random
		// number generator instead.
		data[i] = characters[rand.Intn(len(characters))]
	}
	return string(data)
}

func (u *User) Validate() error {
	switch {
	case u.Username == "":
		return errors.New("username field is empty")
	case len(u.Username) > MaxNameLength:
		return errors.New("username field is too long")
	case u.Username == "default":
		return errors.New("username field is invalid")
	case u.Password == "":
		return errors.New("password is empty")
	default:
		return nil
	}
}

type User struct {
	bun.BaseModel
	ID           int64  `bun:",pk,autoincrement"`
	Username     string `json:"username" bun:",unique"`
	Token        string `json:"-" bun:",unique"`
	Password     string `json:"password,omitempty" bun:"-"`
	PasswordHash string `json:"-"`
}
