package main

import "github.com/urfave/cli/v2"

var pushPrometheusMetrics = &cli.Command{
	Usage:  "Push periodic metrics to prometheus",
	Name:   "push-prometheus-metrics",
	Flags:  []cli.Flag{},
	Action: func(cctx *cli.Context) error { return nil },
}
