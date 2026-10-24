package provider

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
)

// YandexCabinet reads Yandex's own request counts from the Developer's Account API
// (developer.tech.yandex.ru → Connect API → "Developer's Account API" gives the key).
//
// Only Yandex's statistics decide whether a limit was broken (free terms §4.3), and the
// browser asks Yandex more often than we hand out rounds: the locate call, and every step in
// a Move round. So the warning trusts this count; Usage stays as the per-round metric.
type YandexCabinet struct {
	Key    string
	Base   string // empty = https://api-developer.tech.yandex.net
	Client *http.Client
}

// Quota is one of Yandex's daily counters for a service on the account.
type Quota struct {
	Service string // the service's name in the cabinet
	Name    string // Yandex's name for the counter
	Used    int64
	Limit   int64 // -1 = no limit
}

// Near is true at WarnShare of the limit or more.
func (q Quota) Near() bool {
	return q.Limit > 0 && float64(q.Used) >= float64(q.Limit)*WarnShare
}

// Quotas lists every counter of every service in every project of the account.
func (c YandexCabinet) Quotas(ctx context.Context) ([]Quota, error) {
	var projects struct {
		Projects []struct {
			ID string `json:"id"`
		} `json:"projects"`
	}
	if err := c.get(ctx, "/projects", &projects); err != nil {
		return nil, err
	}
	var out []Quota
	for _, p := range projects.Projects {
		var services struct {
			Services []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"services"`
		}
		base := "/projects/" + url.PathEscape(p.ID) + "/services"
		if err := c.get(ctx, base, &services); err != nil {
			return nil, err
		}
		for _, s := range services.Services {
			var limits struct {
				Limits map[string]struct {
					Limit int64 `json:"limit"`
					Value int64 `json:"value"`
				} `json:"limits"`
			}
			if err := c.get(ctx, base+"/"+url.PathEscape(s.ID)+"/limits", &limits); err != nil {
				return nil, err
			}
			for name, l := range limits.Limits {
				out = append(out, Quota{Service: s.Name, Name: name, Used: l.Value, Limit: l.Limit})
			}
		}
	}
	slices.SortFunc(out, func(a, b Quota) int {
		return cmp.Or(cmp.Compare(a.Service, b.Service), cmp.Compare(a.Name, b.Name))
	})
	return out, nil
}

func (c YandexCabinet) get(ctx context.Context, path string, v any) error {
	base := c.Base
	if base == "" {
		base = "https://api-developer.tech.yandex.net"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Auth-Key", c.Key)
	res, err := c.Client.Do(req)
	if err != nil {
		return fmt.Errorf("yandex cabinet: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("yandex cabinet %s: %s", path, res.Status)
	}
	if err := json.NewDecoder(res.Body).Decode(v); err != nil {
		return fmt.Errorf("yandex cabinet %s: %w", path, err)
	}
	return nil
}
