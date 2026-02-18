package connectors_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

type fakeConnector struct {
	key    string
	name   string
	kind   string
	health error
}

func (f *fakeConnector) Key() string                       { return f.key }
func (f *fakeConnector) Name() string                      { return f.name }
func (f *fakeConnector) Kind() string                      { return f.kind }
func (f *fakeConnector) HealthCheck(context.Context) error { return f.health }
func (f *fakeConnector) ResolveByURL(context.Context, string) (*connectors.MangaResult, error) {
	return nil, nil
}
func (f *fakeConnector) SearchByTitle(context.Context, string, int) ([]connectors.MangaResult, error) {
	return nil, nil
}

func TestRegistryRegisterListHealth(t *testing.T) {
	r := connectors.NewRegistry()

	if err := r.Register(&fakeConnector{key: "b", name: "B", kind: connectors.KindNative}); err != nil {
		t.Fatalf("register b: %v", err)
	}
	if err := r.Register(&fakeConnector{key: "a", name: "A", kind: connectors.KindYAML, health: errors.New("down")}); err != nil {
		t.Fatalf("register a: %v", err)
	}

	list := r.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 connectors, got %d", len(list))
	}
	if list[0].Key != "a" || list[1].Key != "b" {
		t.Fatalf("expected sorted keys a,b got %s,%s", list[0].Key, list[1].Key)
	}

	health := r.Health(context.Background())
	if len(health) != 2 {
		t.Fatalf("expected 2 health items, got %d", len(health))
	}
	if health[0].Key != "a" || health[0].Healthy {
		t.Fatalf("expected a unhealthy")
	}
	if health[1].Key != "b" || !health[1].Healthy {
		t.Fatalf("expected b healthy")
	}
}

func TestRegistryGetNormalizesKnownAliasAndFormatting(t *testing.T) {
	r := connectors.NewRegistry()
	if err := r.Register(&fakeConnector{key: "mangafire", name: "MangaFire", kind: connectors.KindNative}); err != nil {
		t.Fatalf("register mangafire: %v", err)
	}
	if err := r.Register(&fakeConnector{key: "asuracomic", name: "AsuraComic", kind: connectors.KindNative}); err != nil {
		t.Fatalf("register asuracomic: %v", err)
	}
	if err := r.Register(&fakeConnector{key: "flamecomics", name: "FlameComics", kind: connectors.KindNative}); err != nil {
		t.Fatalf("register flamecomics: %v", err)
	}
	if err := r.Register(&fakeConnector{key: "webtoons", name: "WEBTOON", kind: connectors.KindNative}); err != nil {
		t.Fatalf("register webtoons: %v", err)
	}

	tests := []string{
		"mangafire",
		" MangaFire ",
		"mangafire.to",
		"https://mangafire.to/manga/bukiyou-na-senpaii.2nw2",
		"www.mangafire.to",
	}

	for _, key := range tests {
		if _, ok := r.Get(key); !ok {
			t.Fatalf("expected connector for key %q", key)
		}
	}

	asuraTests := []string{
		"asuracomic",
		"AsuraComic",
		"asuracomic.net",
		"https://asuracomic.net/series/nano-machine-11b89554",
		"www.asuracomic.net",
	}

	for _, key := range asuraTests {
		if _, ok := r.Get(key); !ok {
			t.Fatalf("expected asuracomic connector for key %q", key)
		}
	}

	flameTests := []string{
		"flamecomics",
		"FlameComics",
		"flamecomics.xyz",
		"https://flamecomics.xyz/series/83",
		"www.flamecomics.xyz",
	}

	for _, key := range flameTests {
		if _, ok := r.Get(key); !ok {
			t.Fatalf("expected flamecomics connector for key %q", key)
		}
	}

	webtoonsTests := []string{
		"webtoons",
		"WebToons",
		"webtoons.com",
		"https://www.webtoons.com/en/romance/maybe-meant-to-be/list?title_no=4208",
		"https://m.webtoons.com/en/romance/maybe-meant-to-be/list?title_no=4208",
	}

	for _, key := range webtoonsTests {
		if _, ok := r.Get(key); !ok {
			t.Fatalf("expected webtoons connector for key %q", key)
		}
	}
}
