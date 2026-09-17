package auth

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/oauth2"
)

type OAuthConfig struct {
	ClientID     string
	ClientSecret string
	AuthURL      string
	TokenURL     string
	Scopes       []string
	RedirectURL  string
}

func (c OAuthConfig) Config() *oauth2.Config {
	return &oauth2.Config{
		ClientID: c.ClientID, ClientSecret: c.ClientSecret,
		Endpoint: oauth2.Endpoint{AuthURL: c.AuthURL, TokenURL: c.TokenURL},
		Scopes:   c.Scopes, RedirectURL: c.RedirectURL,
	}
}

func (c OAuthConfig) AuthCodeURL(state, verifier string) string {
	return c.Config().AuthCodeURL(state, oauth2.SetAuthURLParam("code_challenge", verifier), oauth2.SetAuthURLParam("code_challenge_method", "S256"))
}

func (c OAuthConfig) Exchange(ctx context.Context, code, verifier string) (*oauth2.Token, error) {
	return c.Config().Exchange(ctx, code, oauth2.VerifierOption(verifier))
}

func BearerClient(ctx context.Context, token *oauth2.Token) *http.Client {
	return oauth2.NewClient(ctx, oauth2.StaticTokenSource(token))
}

func NormalizeTokenURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), nil
}
