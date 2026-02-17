package handlers

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/gabriel/cross-site-tracker/backend/internal/models"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
	"github.com/gofiber/fiber/v2"
)

const activeProfileCookieName = "active_profile_id"

type profileContextResolver struct {
	repo *repository.ProfileRepository
}

func newProfileContextResolver(db *sql.DB) *profileContextResolver {
	return &profileContextResolver{repo: repository.NewProfileRepository(db)}
}

func (r *profileContextResolver) Resolve(c *fiber.Ctx) (*models.Profile, error) {
	if profile, err := r.resolveFromQuery(c); err != nil {
		return nil, err
	} else if profile != nil {
		r.setActiveProfileCookie(c, profile.ID)
		return profile, nil
	}

	if profile, err := r.resolveFromHeaders(c); err != nil {
		return nil, err
	} else if profile != nil {
		r.setActiveProfileCookie(c, profile.ID)
		return profile, nil
	}

	if profile, err := r.resolveFromCookie(c); err != nil {
		return nil, err
	} else if profile != nil {
		r.setActiveProfileCookie(c, profile.ID)
		return profile, nil
	}

	profile, err := r.repo.GetDefault()
	if err != nil {
		return nil, fmt.Errorf("resolve default profile: %w", err)
	}
	if profile == nil {
		return nil, fmt.Errorf("no profiles available")
	}

	r.setActiveProfileCookie(c, profile.ID)
	return profile, nil
}

func (r *profileContextResolver) ListProfiles() ([]models.Profile, error) {
	return r.repo.List()
}

func (r *profileContextResolver) resolveFromQuery(c *fiber.Ctx) (*models.Profile, error) {
	raw := strings.TrimSpace(c.Query("profile"))
	if raw == "" {
		return nil, nil
	}

	profile, err := r.lookup(raw)
	if err != nil {
		return nil, err
	}
	if profile == nil {
		return nil, fmt.Errorf("invalid profile")
	}
	return profile, nil
}

func (r *profileContextResolver) resolveFromHeaders(c *fiber.Ctx) (*models.Profile, error) {
	if rawID := strings.TrimSpace(c.Get("X-Profile-ID")); rawID != "" {
		profile, err := r.lookup(rawID)
		if err != nil {
			return nil, err
		}
		if profile == nil {
			return nil, fmt.Errorf("invalid X-Profile-ID")
		}
		return profile, nil
	}

	if rawKey := strings.TrimSpace(c.Get("X-Profile-Key")); rawKey != "" {
		profile, err := r.lookup(rawKey)
		if err != nil {
			return nil, err
		}
		if profile == nil {
			return nil, fmt.Errorf("invalid X-Profile-Key")
		}
		return profile, nil
	}

	return nil, nil
}

func (r *profileContextResolver) resolveFromCookie(c *fiber.Ctx) (*models.Profile, error) {
	raw := strings.TrimSpace(c.Cookies(activeProfileCookieName))
	if raw == "" {
		return nil, nil
	}

	profile, err := r.lookup(raw)
	if err != nil {
		return nil, err
	}
	if profile == nil {
		return nil, nil
	}
	return profile, nil
}

func (r *profileContextResolver) lookup(value string) (*models.Profile, error) {
	if id, err := strconv.ParseInt(value, 10, 64); err == nil && id > 0 {
		item, lookupErr := r.repo.GetByID(id)
		if lookupErr != nil {
			return nil, fmt.Errorf("lookup profile by id: %w", lookupErr)
		}
		return item, nil
	}

	item, err := r.repo.GetByKey(value)
	if err != nil {
		return nil, fmt.Errorf("lookup profile by key: %w", err)
	}
	return item, nil
}

func (r *profileContextResolver) setActiveProfileCookie(c *fiber.Ctx, profileID int64) {
	c.Cookie(&fiber.Cookie{
		Name:     activeProfileCookieName,
		Value:    strconv.FormatInt(profileID, 10),
		Path:     "/",
		HTTPOnly: true,
		SameSite: fiber.CookieSameSiteLaxMode,
	})
}
