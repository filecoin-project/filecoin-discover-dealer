package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"

	"github.com/labstack/echo/v4"
)

func reqOk(c echo.Context, f string, args ...interface{}) error {
	return c.String(
		http.StatusOK,
		string(wrapComment(fmt.Sprintf(f, args...), true)),
	)
}

func reqFail(c echo.Context, f string, args ...interface{}) error {
	return c.String(
		http.StatusTeapot, // DO NOT use 400: we rewrite that on the nginx level to normalize a class of transport errors
		string(wrapComment(fmt.Sprintf(f, args...), false)),
	)
}

func authFail(c echo.Context, f string, args ...interface{}) error {
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
