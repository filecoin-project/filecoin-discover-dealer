package main

import (
	"net/http"
	"os"

	"github.com/filecoin-project/filecoin-discover-dealer/ddcommon"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	lslog "github.com/labstack/gommon/log"
	"github.com/urfave/cli/v2"
)

func main() {

	//
	// Server setup
	e := echo.New()

	e.GET("/hello", apiHello)
	e.POST("/hello", apiHello)
	e.Any("*", func(c echo.Context) error { return c.NoContent(http.StatusForbidden) })

	e.Logger.SetLevel(lslog.INFO)
	e.Use(middleware.Logger())
	// this is our auth-provider
	e.Use(spidAuth)
	// abomination to ensure we do not write out JSON on errors, only commented-out text
	e.JSONSerializer = new(textyJSON)

	//
	// Housekeeping
	ctx, cleanup := ddcommon.TopContext(
		func() { e.Close() }, //nolint:errcheck
	)
	defer cleanup()

	//
	// Boot up
	e.Logger.Info(
		(&cli.App{
			Name:   "discover-dealed-webapi",
			Before: ddcommon.CliBeforeSetup,
			Action: func(cctx *cli.Context) error { return e.Start(cctx.String("webapi-listen-address")) },
			Flags:  ddcommon.CliFlags,
		}).RunContext(ctx, os.Args),
	)
}
