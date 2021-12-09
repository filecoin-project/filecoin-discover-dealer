package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"

	"github.com/filecoin-project/filecoin-discover-dealer/ddcommon"
	filaddr "github.com/filecoin-project/go-address"
	"github.com/labstack/echo/v4"
)

func ineligibleSpMsg(sp filaddr.Address) string {
	return fmt.Sprintf(
		`
At the time of this request Storage provider %s is not eligible to participate in filecoin-discover
( this state is is almost certainly *temporary* )

Make sure that you:
- Are currently able to win block-rewards ( you have sufficient quality-adjusted power and have not faulted in the past 48h )
- Are continuing to serve previously onboarded filecoin-discover datasets free of charge
- Have associated your SP with a slack contact via https://filecoin-discover.web3.storage/hello?filslack=YOURSLACKHANDLE


If the problem persists please contact us in #fil-discover-support over at the Filecoin Slack https://filecoin.io/slack
`,
		sp.String(),
	)
}

func ineligibleManifestMsg(manifest string) string {
	return fmt.Sprintf(
		`
It looks like drive described by manifest %s does NOT contain any car files eligible for deal proposals.
This might indicate an inaccuracy in our records we need to fix manually!

Please contact us in #fil-discover-support over at the Filecoin Slack https://filecoin.io/slack
`,
		manifest,
	)
}

func httpOk(c echo.Context, f string, args ...interface{}) error {
	return c.String(
		http.StatusOK,
		string(wrapComment(fmt.Sprintf(f, args...), true)),
	)
}

func httpFail(c echo.Context, internalReason interface{}, fMsg string, args ...interface{}) error {

	if reqUUID := c.Response().Header().Get("X-REQUEST-UUID"); reqUUID != "" {

		outJ, err := json.Marshal(struct {
			Err         string      `json:"error"`
			ErrInternal interface{} `json:"internal,omitempty"`
		}{
			Err:         fmt.Sprintf(fMsg, args...),
			ErrInternal: internalReason,
		})
		if err != nil {
			return err
		}

		if _, err = ddcommon.Db.Exec(
			c.Request().Context(),
			`UPDATE requests SET meta = $1 WHERE request_uuid = $2`,
			outJ,
			reqUUID,
		); err != nil {
			return err
		}

	}

	return c.String(
		http.StatusTeapot, // DO NOT use 400: we rewrite that on the nginx level to normalize a class of transport errors
		string(wrapComment(fmt.Sprintf(fMsg, args...), false)),
	)
}

func httpAuthFail(c echo.Context, f string, args ...interface{}) error {
	c.Response().Header().Set(echo.HeaderWWWAuthenticate, authScheme)
	return c.String(
		http.StatusUnauthorized,
		string(wrapComment(
			fmt.Sprintf(
				echo.ErrUnauthorized.Error()+"\n"+f,
				args...,
			),
			false,
		)),
	)
}

// abomination to ensure we do not write out JSON or any command-like thing,
// only commented-out text
type textyJSON struct{}

func (*textyJSON) Deserialize(c echo.Context, i interface{}) error {
	return new(echo.DefaultJSONSerializer).Deserialize(c, i)
}

func (*textyJSON) Serialize(c echo.Context, i interface{}, indent string) error {

	if c.Request().Header.Get(echo.HeaderAccept) == echo.MIMEApplicationJSON {
		return new(echo.DefaultJSONSerializer).Serialize(c, i, indent)
	}

	w := new(bytes.Buffer)
	enc := json.NewEncoder(w)
	if indent != "" {
		enc.SetIndent("", indent)
	}
	err := enc.Encode(i)
	if err != nil {
		return err
	}

	_, err = c.Response().Write(wrapComment(w.String(), c.Response().Status < 400))
	return err
}

var (
	commentRe     = regexp.MustCompile(`(?m)^`)
	commentPrefix = "# "
)

func wrapComment(input string, success bool) []byte {

	out := append(
		make([]byte, 0, len(input)+100),
		"\n#\n"...,
	)
	out = append(
		out,
		commentRe.ReplaceAllString(input, commentPrefix)...,
	)

	if success {
		out = append(
			out,
			"\n#\n\nexit 0;\n"...,
		)
	} else {
		out = append(
			out,
			"\n#\n\nexit 1;\n"...,
		)
	}

	return out
}
