package main

import (
	"encoding/json"
	"fmt"

	ddcommon "github.com/filecoin-project/filecoin-discover-dealer/ddcommon"
	"github.com/labstack/echo/v4"
)

func apiHello(c echo.Context) error {
	p := c.QueryParams()
	slackHandle := p.Get("filslack")
	if len(slackHandle) < 3 || len(slackHandle) > 128 {
		return httpFail(c, nil, `You must provide your filecoin-slack handle as a 'filslack' parameter when introducing yourself: .../hello?filslack=YOURSLACKHANDLE`)
	} else if slackHandle == "YOURSLACKHANDLE" {
		return httpFail(c, nil, `You must provide your *actual* filecoin-slack @name as a 'filslack' parameter when introducing yourself: .../hello?filslack=YOURSLACKHANDLE`)
	} else if slackHandle[0:2] == "U0" {
		return httpFail(c, nil, `You must provide your *actual* filecoin-slack @name when introducing yourself, not the U0… numeric id: .../hello?filslack=YOURSLACKHANDLE`)
	}

	jstr, _ := json.Marshal(slackHandle)

	sp := c.Response().Header().Get("X-FIL-SPID")

	if _, err := ddcommon.Db.Exec(
		c.Request().Context(),
		`
		INSERT INTO discover.providers ( provider, meta ) VALUES ( $1, $2 )
			ON CONFLICT ( provider ) DO UPDATE SET
				meta = JSONB_SET(
					providers.meta,
					'{ filslack }',
					( providers.meta->'filslack' ) || ( EXCLUDED.meta->'filslack' ),
					TRUE
				)
		`,
		sp,
		fmt.Sprintf(`{ "filslack": { %s: true } }`, jstr),
	); err != nil {
		return err
	}

	return httpOk(
		c,
		"Well, hello there `%s`, you have been associated with storage provider %s!",
		slackHandle,
		sp,
	)
}
