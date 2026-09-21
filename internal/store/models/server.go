package models

import (
	"time"

	"github.com/xmpanel/xmpanel/internal/adapter"
)

// Server is a registered backend. Credentials never leave the store in JSON.
type Server struct {
	ID             int64                  `json:"id" db:"id"`
	Name           string                 `json:"name" db:"name"`
	Protocol       adapter.Protocol       `json:"protocol" db:"protocol"`
	Implementation adapter.Implementation `json:"implementation" db:"implementation"`
	Endpoint       string                 `json:"endpoint" db:"endpoint"`
	Domain         string                 `json:"domain" db:"domain"`
	Enabled        bool                   `json:"enabled" db:"enabled"`
	CreatedAt      time.Time              `json:"created_at" db:"created_at"`
	UpdatedAt      time.Time              `json:"updated_at" db:"updated_at"`
}

type CreateServerRequest struct {
	Name           string                 `json:"name"`
	Protocol       adapter.Protocol       `json:"protocol"`
	Implementation adapter.Implementation `json:"implementation"`
	Endpoint       string                 `json:"endpoint"`
	Domain         string                 `json:"domain"`
	Credentials    *adapter.Credentials   `json:"credentials"`
}

type UpdateServerRequest struct {
	Name        *string              `json:"name,omitempty"`
	Endpoint    *string              `json:"endpoint,omitempty"`
	Domain      *string              `json:"domain,omitempty"`
	Credentials *adapter.Credentials `json:"credentials,omitempty"`
	Enabled     *bool                `json:"enabled,omitempty"`
}
