package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/xmpanel/xmpanel/internal/config"
)

var (
	ErrInvalidToken     = errors.New("invalid token")
	ErrExpiredToken     = errors.New("token has expired")
	ErrInvalidClaims    = errors.New("invalid token claims")
	ErrTokenNotYetValid = errors.New("token not yet valid")
)

// TokenType represents the type of JWT token
type TokenType string

const (
	TokenTypeAccess  TokenType = "access"
	TokenTypeRefresh TokenType = "refresh"
)

// Claims represents the JWT claims
type Claims struct {
	jwt.RegisteredClaims
	UserID    int64     `json:"uid"`
	Username  string    `json:"usr"`
	Role      string    `json:"role"`
	TokenType TokenType `json:"type"`
	SessionID string    `json:"sid"`
	DeviceID  string    `json:"did,omitempty"`
}

// JWTManager handles JWT token operations
type JWTManager struct {
	secret          []byte
	accessTokenTTL  time.Duration
	refreshTokenTTL time.Duration
	issuer          string
}

// NewJWTManager creates a new JWT manager
func NewJWTManager(cfg config.JWTConfig) *JWTManager {
	return &JWTManager{
		secret:          []byte(cfg.Secret),
		accessTokenTTL:  cfg.AccessTokenTTL,
		refreshTokenTTL: cfg.RefreshTokenTTL,
		issuer:          cfg.Issuer,
	}
}

// TokenPair represents an access and refresh token pair
type TokenPair struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	TokenType    string    `json:"token_type"`
}

// GenerateTokenPair generates both access and refresh tokens
func (m *JWTManager) GenerateTokenPair(userID int64, username, role, sessionID, deviceID string) (*TokenPair, error) {
	now := time.Now()

	// Generate access token
	accessClaims := &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    m.issuer,
			Subject:   fmt.Sprintf("%d", userID),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(m.accessTokenTTL)),
			NotBefore: jwt.NewNumericDate(now),
		},
		UserID:    userID,
		Username:  username,
		Role:      role,
		TokenType: TokenTypeAccess,
		SessionID: sessionID,
		DeviceID:  deviceID,
	}

	accessToken := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims)
	accessTokenString, err := accessToken.SignedString(m.secret)
	if err != nil {
		return nil, fmt.Errorf("failed to sign access token: %w", err)
	}

	// Generate refresh token
	refreshClaims := &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    m.issuer,
			Subject:   fmt.Sprintf("%d", userID),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(m.refreshTokenTTL)),
			NotBefore: jwt.NewNumericDate(now),
		},
		UserID:    userID,
		Username:  username,
		Role:      role,
		TokenType: TokenTypeRefresh,
		SessionID: sessionID,
		DeviceID:  deviceID,
	}

	refreshToken := jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims)
	refreshTokenString, err := refreshToken.SignedString(m.secret)
	if err != nil {
		return nil, fmt.Errorf("failed to sign refresh token: %w", err)
	}

	return &TokenPair{
		AccessToken:  accessTokenString,
		RefreshToken: refreshTokenString,
		ExpiresAt:    now.Add(m.accessTokenTTL),
		TokenType:    "Bearer",
	}, nil
}

// ValidateToken validates a JWT token and returns the claims
func (m *JWTManager) ValidateToken(tokenString string, expectedType TokenType) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return m.secret, nil
	})

	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrExpiredToken
		}
		if errors.Is(err, jwt.ErrTokenNotValidYet) {
			return nil, ErrTokenNotYetValid
		}
		return nil, ErrInvalidToken
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, ErrInvalidClaims
	}

	if claims.TokenType != expectedType {
		return nil, ErrInvalidToken
	}

	if claims.Issuer != m.issuer {
		return nil, ErrInvalidToken
	}

	return claims, nil
}
