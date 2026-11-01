package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/gabriel/cross-site-tracker/backend/internal/models"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
	"github.com/gofiber/fiber/v2"
)

const activeProfileCookieName = "active_profile_id"

// unknownProfileError is a request naming a profile that does not exist — the
// caller's mistake, and a 400. It is a type of its own so it can be told apart
// from a lookup that failed: both used to come back as one undifferentiated
// error, so a database fault was answered "Invalid profile" and logged only at
// debug level, which left the Pi's logs empty while every request failed. The
// text stays exactly what it was, because the JSON API returns it verbatim.
type unknownProfileError string

func (e unknownProfileError) Error() string { return string(e) }

// isUnknownProfile reports whether Resolve failed because the request named no
// existing profile, as opposed to the profile lookup itself failing.
func isUnknownProfile(err error) bool {
	var unknown unknownProfileError
	return errors.As(err, &unknown)
}

// failProfile answers a request whose profile could not be resolved: a profile
// that does not exist is a 400, anything else is the database failing and a
// 500, which fail logs at error level where the operator will see it.
func (h *DashboardHandler) failProfile(c *fiber.Ctx, err error) error {
	if isUnknownProfile(err) {
		return h.fail(c, fiber.StatusBadRequest, "Invalid profile", err)
	}
	return h.fail(c, fiber.StatusInternalServerError, "Failed to load profile", err)
}

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

	profile, err := r.repo.GetDefault(c.Context())
	if err != nil {
		return nil, fmt.Errorf("resolve default profile: %w", err)
	}
	if profile == nil {
		return nil, unknownProfileError("no profiles available")
	}

	r.setActiveProfileCookie(c, profile.ID)
	return profile, nil
}

func (r *profileContextResolver) ListProfiles(ctx context.Context) ([]models.Profile, error) {
	return r.repo.List(ctx)
}

func (r *profileContextResolver) resolveFromQuery(c *fiber.Ctx) (*models.Profile, error) {
	raw := strings.TrimSpace(c.Query("profile"))
	if raw == "" {
		return nil, nil
	}

	profile, err := r.lookup(c.Context(), raw)
	if err != nil {
		return nil, err
	}
	if profile == nil {
		return nil, unknownProfileError("invalid profile")
	}
	return profile, nil
}

func (r *profileContextResolver) resolveFromHeaders(c *fiber.Ctx) (*models.Profile, error) {
	if rawID := strings.TrimSpace(c.Get("X-Profile-ID")); rawID != "" {
		profile, err := r.lookup(c.Context(), rawID)
		if err != nil {
			return nil, err
		}
		if profile == nil {
			return nil, unknownProfileError("invalid X-Profile-ID")
		}
		return profile, nil
	}

	if rawKey := strings.TrimSpace(c.Get("X-Profile-Key")); rawKey != "" {
		profile, err := r.lookup(c.Context(), rawKey)
		if err != nil {
			return nil, err
		}
		if profile == nil {
			return nil, unknownProfileError("invalid X-Profile-Key")
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

	profile, err := r.lookup(c.Context(), raw)
	if err != nil {
		return nil, err
	}
	if profile == nil {
		return nil, nil
	}
	return profile, nil
}

func (r *profileContextResolver) lookup(ctx context.Context, value string) (*models.Profile, error) {
	if id, err := strconv.ParseInt(value, 10, 64); err == nil && id > 0 {
		item, lookupErr := r.repo.GetByID(ctx, id)
		if lookupErr != nil {
			return nil, fmt.Errorf("lookup profile by id: %w", lookupErr)
		}
		return item, nil
	}

	item, err := r.repo.GetByKey(ctx, value)
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
