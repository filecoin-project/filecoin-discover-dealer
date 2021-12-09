package main

import (
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/filecoin-project/filecoin-discover-dealer/ddcommon"
	filaddr "github.com/filecoin-project/go-address"
	lotusapi "github.com/filecoin-project/lotus/api"
	"github.com/urfave/cli/v2"
	"golang.org/x/net/context"
)

var proposePending = &cli.Command{
	Usage: "Propose pending deals to providers",
	Name:  "propose-pending",
	Flags: []cli.Flag{},
	Action: func(cctx *cli.Context) error {

		ctx := cctx.Context

		countDone := new(int32)
		countFails := new(int32)
		props := make(map[filaddr.Address][]*lotusapi.StartDealParams, 16)
		defer func() {
			log.Infow("summary",
				"uniqueMiners", len(props),
				"successful", atomic.LoadInt32(countDone),
				"failed", atomic.LoadInt32(countFails),
			)
		}()

		rows, err := ddcommon.Db.Query(
			ctx,
			`
			SELECT dealstart_payload
				FROM discover.proposals
			WHERE
				proposal_success_cid IS NULL
					AND
				proposal_failure = ''
			`,
		)
		if err != nil {
			return err
		}

		for rows.Next() {
			var j []byte
			if err = rows.Scan(&j); err != nil {
				return err
			}

			p := new(lotusapi.StartDealParams)
			if err = json.Unmarshal(j, &p); err != nil {
				return err
			}

			if _, exists := props[p.Miner]; !exists {
				props[p.Miner] = make([]*lotusapi.StartDealParams, 0, 128)
			}
			props[p.Miner] = append(props[p.Miner], p)
		}
		if err = rows.Err(); err != nil {
			return err
		}
		rows.Close()

		if len(props) == 0 {
			return nil
		}

		var wg sync.WaitGroup
		for _, spProps := range props {
			spProps := spProps
			wg.Add(1)
			go func() {
				defer wg.Done()

				for _, p := range spProps {

					tCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
					defer cancel()

					propCid, apiErr := ddcommon.LotusAPI.ClientStatelessDeal(
						tCtx,
						p,
					)

					var dbErr error

					if apiErr != nil {
						atomic.AddInt32(countFails, 1)
						_, dbErr = ddcommon.Db.Exec(
							ctx,
							`
							UPDATE discover.proposals
								SET proposal_failure = $1
							WHERE
								provider = $2
									AND
								piece_cid = $3
									AND
								proposal_failure = ''
									AND
								proposal_success_cid IS NULL
							`,
							fmt.Sprintf("%d : %s", time.Now().UnixNano(), apiErr.Error()),
							p.Miner.String(),
							p.Data.PieceCid.String(),
						)
					} else {
						atomic.AddInt32(countDone, 1)
						_, dbErr = ddcommon.Db.Exec(
							ctx,
							`
							UPDATE discover.proposals
								SET proposal_success_cid = $1
							WHERE
								provider = $2
									AND
								piece_cid = $3
									AND
								proposal_failure = ''
									AND
								proposal_success_cid IS NULL
							`,
							propCid.String(),
							p.Miner.String(),
							p.Data.PieceCid.String(),
						)
					}

					if dbErr != nil {
						log.Warnw("unexpected error updating proposal: %s", dbErr.Error(), "proposal", p)
					}

					select {
					case <-ctx.Done():
						return
					case <-time.After(5 * time.Second):
					}
				}
			}()
		}

		wg.Wait()

		return nil
	},
}
