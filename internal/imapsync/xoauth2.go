package imapsync

import (
	"fmt"

	"github.com/emersion/go-sasl"
)

// XOAUTH2 SASL client (Gmail / Office365 IMAP).
type xoauth2Client struct {
	username string
	token    string
	step     int
}

func newXOAUTH2Client(username, token string) sasl.Client {
	return &xoauth2Client{username: username, token: token}
}

func (a *xoauth2Client) Start() (mech string, ir []byte, err error) {
	resp := fmt.Sprintf("user=%s\x01auth=Bearer %s\x01\x01", a.username, a.token)
	return "XOAUTH2", []byte(resp), nil
}

func (a *xoauth2Client) Next(challenge []byte) ([]byte, error) {
	if a.step == 0 && len(challenge) > 0 {
		a.step++
		return []byte{}, fmt.Errorf("xoauth2: %s", string(challenge))
	}
	return nil, fmt.Errorf("unexpected server challenge")
}
