package server

import (
	"github.com/pacepoker/poker/internal/store"
	"github.com/pacepoker/poker/internal/user"
)

// NewUserServer returns a UserServiceServer backed by the given store.
func NewUserServer(st store.Store) *user.Service {
	return user.New(st)
}
