package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/abs"
	"github.com/vavallee/bindery/internal/grimmory"
	"github.com/vavallee/bindery/internal/models"
)

// Every admin-typed URL validator must follow the same rule (#2036): link-local
// only with the option on, the refusal naming the variable when it is off, and
// cloud metadata never. Reverting any site to the old policy fails here.
func TestServiceURLValidators_LinkLocalRule(t *testing.T) {
	type validator struct {
		name string
		run  func(t *testing.T, host string) error
	}
	body := func(rec *httptest.ResponseRecorder) error {
		if rec.Code >= 200 && rec.Code < 300 {
			return nil
		}
		return errString(rec.Body.String())
	}
	validators := []validator{
		{"grimmory", func(t *testing.T, host string) error {
			_, err := grimmory.ValidateBaseURLSecure("http://" + host + ":6060")
			return err
		}},
		{"audiobookshelf", func(t *testing.T, host string) error {
			_, err := abs.ValidateBaseURLSecure("http://" + host + ":13378")
			return err
		}},
		{"calibre plugin setting", func(t *testing.T, host string) error {
			return validateSettingValue(SettingCalibrePluginURL, "http://"+host+":8099")
		}},
		{"audiobookshelf setting", func(t *testing.T, host string) error {
			return validateSettingValue(SettingABSBaseURL, "http://"+host+":13378")
		}},
		{"download client create", func(t *testing.T, host string) error {
			h, _ := downloadClientFixture(t)
			rec := httptest.NewRecorder()
			h.Create(rec, httptest.NewRequest(http.MethodPost, "/downloadclient",
				bytes.NewBufferString(`{"name":"q","type":"qbittorrent","host":"`+host+`","port":8080,"username":"u","password":"p","enabled":true}`)))
			return body(rec)
		}},
		{"download client update", func(t *testing.T, host string) error {
			h, clients := downloadClientFixture(t)
			c := &models.DownloadClient{Name: "q", Type: "qbittorrent", Host: "10.0.0.5", Port: 8080, Username: "u", Password: "p", Enabled: true}
			if err := clients.Create(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPut, "/downloadclient/1",
				bytes.NewBufferString(`{"name":"q","type":"qbittorrent","host":"`+host+`","port":8080,"username":"u","password":"p","enabled":true}`))
			rec := httptest.NewRecorder()
			h.Update(rec, withURLParam(req, "id", "1"))
			return body(rec)
		}},
	}

	for _, v := range validators {
		t.Run(v.name, func(t *testing.T) {
			t.Setenv(linkLocalEnv, "")
			if err := v.run(t, "169.254.1.2"); err == nil || !strings.Contains(err.Error(), linkLocalEnv) {
				t.Errorf("option off: err = %v, want a refusal naming %s", err, linkLocalEnv)
			}
			if err := v.run(t, "169.254.169.254"); err == nil {
				t.Error("option off: metadata accepted")
			}
			t.Setenv(linkLocalEnv, "1")
			if err := v.run(t, "169.254.1.2"); err != nil {
				t.Errorf("option on: link-local refused: %v", err)
			}
			if err := v.run(t, "169.254.169.254"); err == nil {
				t.Error("option on: metadata accepted")
			}
		})
	}
}

type errString string

func (e errString) Error() string { return string(e) }
