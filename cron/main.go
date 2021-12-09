package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"time"

	"github.com/filecoin-project/filecoin-discover-dealer/ddcommon"
	fslock "github.com/ipfs/go-fs-lock"
	logging "github.com/ipfs/go-log/v2"
	"github.com/prometheus/client_golang/prometheus"
	prometheuspush "github.com/prometheus/client_golang/prometheus/push"
	"github.com/urfave/cli/v2"
)

var (
	nonAlpha = regexp.MustCompile(`[^a-zA-Z0-9]+`)
	log      = logging.Logger(fmt.Sprintf("discover-dealer-cron(%d)", os.Getpid()))
)

func main() {
	logging.SetLogLevel("*", "INFO") //nolint:errcheck

	ctx, cleanup := ddcommon.TopContext(nil)
	defer cleanup()

	// wrap in a defer to always capture endstate/send a metric, even under panic()s
	var (
		t0             time.Time
		err            error
		currentCmd     string
		currentCmdLock io.Closer
	)
	defer func() {

		// shared log/metric emitter
		// ( lock-contention does not count, see invocation below )
		emitEndLogs := func(logSuccess bool) {

			took := time.Since(t0).Truncate(time.Millisecond)
			cmdPrefix := nonAlpha.ReplaceAllString("discover_dealer_cron_"+currentCmd, `_`)
			logHdr := fmt.Sprintf("=== FINISH '%s' run", currentCmd)
			logArgs := []interface{}{
				"success", logSuccess,
				"took", took.String(),
			}

			tookGauge := prometheus.NewGauge(prometheus.GaugeOpts{
				Name: fmt.Sprintf("%s_run_time", cmdPrefix),
				Help: "How long did the job take (in milliseconds)",
			})
			tookGauge.Set(float64(took.Milliseconds()))
			successGauge := prometheus.NewGauge(prometheus.GaugeOpts{
				Name: fmt.Sprintf("%s_success", cmdPrefix),
				Help: "Whether the job completed with success(1) or failure(0)",
			})

			if logSuccess {
				log.Infow(logHdr, logArgs...)
				successGauge.Set(1)
			} else {
				log.Warnw(logHdr, logArgs...)
				successGauge.Set(0)
			}

			if currentCmd != "track-deals" &&
				currentCmd != "poll-manifests" &&
				currentCmd != "propose-pending" {
				log.Warn("push of prometheus metrics temp-disabled")
				return
			}

			if promErr := prometheuspush.New(ddcommon.PromURL, cmdPrefix).
				BasicAuth(ddcommon.PromUser, ddcommon.PromPass).
				Collector(tookGauge).
				Collector(successGauge).
				Push(); promErr != nil {
				log.Warnf("push of prometheus metrics to '%s' failed: %s", ddcommon.PromURL, promErr)
			}
		}

		// a panic condition takes precedence
		if r := recover(); r != nil {
			if err == nil {
				err = fmt.Errorf("panic encountered: %s", r)
			} else {
				err = fmt.Errorf("panic encountered (in addition to error '%s'): %s", err, r)
			}
		}

		if err != nil {
			// if we are not interactive - be quiet on a failed lock
			if !ddcommon.IsTerm && errors.As(err, new(fslock.LockedError)) {
				cleanup()
				os.Exit(1)
			}

			log.Error(err)
			if currentCmdLock != nil {
				emitEndLogs(false)
			}
			cleanup()
			os.Exit(1)
		} else if currentCmdLock != nil {
			emitEndLogs(true)
		}
	}()

	t0 = time.Now()
	// the function ends after this block, err is examined in the defer above
	err = (&cli.App{
		Name:  "discover-dealer-cron",
		Usage: "Misc background processes for discover-dealer",
		Commands: []*cli.Command{
			pollManifests,
			trackDeals,
			pushPrometheusMetrics,
			proposePending,
		},
		Flags: ddcommon.CliFlags,
		// obtains locks and emits the proper init loglines
		Before: func(cctx *cli.Context) error {
			if err := ddcommon.CliBeforeSetup(cctx); err != nil {
				return err
			}

			// figure out what is the command that was invoked
			if len(os.Args) > 1 {

				cmdNames := make(map[string]string)
				for _, c := range cctx.App.Commands {
					cmdNames[c.Name] = c.Name
					for _, a := range c.Aliases {
						cmdNames[a] = c.Name
					}
				}

				var firstCmdOccurrence string
				for i := 1; i < len(os.Args); i++ {

					// if we are in help context - no locks and no start/stop timers
					if os.Args[i] == `-h` || os.Args[i] == `--help` {
						return nil
					}

					if firstCmdOccurrence != "" {
						continue
					}
					firstCmdOccurrence = cmdNames[os.Args[i]]
				}

				// help, wrong cmd or something
				if firstCmdOccurrence == "" || firstCmdOccurrence == "help" {
					return nil
				}

				currentCmd = firstCmdOccurrence

				var err error
				if currentCmdLock, err = fslock.Lock(os.TempDir(), "ddcron-"+currentCmd); err != nil {
					return err
				}
				log.Infow(fmt.Sprintf("=== BEGIN '%s' run", currentCmd))
			}

			return nil
		},
	}).RunContext(ctx, os.Args)
}
