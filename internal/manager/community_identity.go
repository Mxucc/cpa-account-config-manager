package manager

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

// Who a community account is.
//
// A community provider's credential is often a token that already names the
// account, and this plugin was only reporting the vendor, so a signed-in account was
// indistinguishable from the placeholder it replaced. Reading the identity the
// provider itself put in the token gives the row a name and an email without another
// request and without storing anything derived from the secret.

// communityCredentialIdentity reads the identity claims a token carries. It returns
// empty strings for a token that is not a JWT or names nobody, so a caller can fall
// back instead of inventing a name.
func communityCredentialIdentity(credential string) (email, name string) {
	parts := strings.Split(strings.TrimSpace(credential), ".")
	if len(parts) < 2 {
		return "", ""
	}
	payload, errDecode := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if errDecode != nil {
		// A padded segment is legal too; try the padded decoder before giving up.
		payload, errDecode = base64.URLEncoding.DecodeString(parts[1])
		if errDecode != nil {
			return "", ""
		}
	}
	var claims map[string]any
	if errUnmarshal := json.Unmarshal(payload, &claims); errUnmarshal != nil {
		return "", ""
	}
	read := func(keys ...string) string {
		for _, key := range keys {
			if value, ok := claims[key].(string); ok {
				if trimmed := strings.TrimSpace(value); trimmed != "" {
					return trimmed
				}
			}
		}
		return ""
	}
	email = read("email", "preferred_username", "upn")
	name = read("name", "nickname", "given_name")
	return email, name
}

// communityCredentialLabel is what the account row shows for one credential: the
// email when there is one, else the name, else nothing.
func communityCredentialLabel(credential string) string {
	email, name := communityCredentialIdentity(credential)
	if email != "" {
		return email
	}
	return name
}

// communityChannelHasCredential reports whether the channel this plugin published for
// a provider carries a credential, which is what "signed in" means for it.
func (a *App) communityChannelHasCredential(providerID string) bool {
	_, credential, errChannel := a.communityOwnChannelCredential(providerID)
	return errChannel == nil && strings.TrimSpace(credential) != ""
}

// communityAccountLabel reads the identity a provider's credential carries.
func (a *App) communityAccountLabel(providerID string) string {
	_, credential, errChannel := a.communityOwnChannelCredential(providerID)
	if errChannel != nil {
		return ""
	}
	return communityCredentialLabel(credential)
}
