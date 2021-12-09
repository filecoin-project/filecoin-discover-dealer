package main

import (
	"fmt"
	"strconv"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/filecoin-project/filecoin-discover-dealer/ddcommon"
	filaddr "github.com/filecoin-project/go-address"
	filabi "github.com/filecoin-project/go-state-types/abi"
	filbig "github.com/filecoin-project/go-state-types/big"
	lotusapi "github.com/filecoin-project/lotus/api"
	filprovider "github.com/filecoin-project/lotus/chain/actors/builtin/miner"
	filtypes "github.com/filecoin-project/lotus/chain/types"
	"github.com/ipfs/go-cid"
	"github.com/urfave/cli/v2"
)

var trackDeals = &cli.Command{
	Usage: "Track state of filecoin deals related to known PieceCIDs",
	Name:  "track-deals",
	Flags: []cli.Flag{},
	Action: func(cctx *cli.Context) error {

		ctx := cctx.Context

		var stateTipset *filtypes.TipSet
		var stateDeals map[string]lotusapi.MarketDeal
		dealsQueried := make(chan error, 1)
		go func() {

			defer close(dealsQueried)

			var err error
			stateTipset, err = ddcommon.LotusLookbackTipset(ctx)
			if err != nil {
				dealsQueried <- err
				return
			}

			log.Infow("retrieving Market Deals from", "state", stateTipset.Key(), "epoch", stateTipset.Height(), "wallTime", time.Unix(int64(stateTipset.Blocks()[0].Timestamp), 0))
			stateDeals, err = ddcommon.LotusAPI.StateMarketDeals(ctx, stateTipset.Key())
			if err != nil {
				dealsQueried <- err
				return
			}

			log.Infof("retrieved %s state deal records", humanize.Comma(int64(len(stateDeals))))
		}()

		knownClients := make(map[filaddr.Address]*filbig.Int)
		rows, err := ddcommon.Db.Query(
			ctx,
			`SELECT client FROM clients`,
		)
		if err != nil {
			return err
		}
		for rows.Next() {
			var c string
			if err = rows.Scan(&c); err != nil {
				return err
			}
			cAddr, err := filaddr.NewFromString(c)
			if err != nil {
				return err
			}

			dcap, err := ddcommon.LotusAPI.StateVerifiedClientStatus(ctx, cAddr, stateTipset.Key())
			if err != nil {
				return err
			}
			knownClients[cAddr] = dcap
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()

		log.Infof("queried datacap for %d clients", len(knownClients))

		knownPieces := make(map[cid.Cid]struct{}, 5_000_000)

		type filDeal struct {
			pieceCid cid.Cid
			status   string
		}
		knownDeals := make(map[int64]filDeal)

		rows, err = ddcommon.Db.Query(
			ctx,
			`
			SELECT c.piece_cid, d.deal_id, d.status
				FROM discover.car_files c
				LEFT JOIN discover.published_deals d USING ( piece_cid )
			`,
		)
		if err != nil {
			return err
		}
		for rows.Next() {
			var pCidStr string
			var dealID *int64
			var dealStatus *string

			if err = rows.Scan(&pCidStr, &dealID, &dealStatus); err != nil {
				return err
			}
			pCid, err := cid.Parse(pCidStr)
			if err != nil {
				return err
			}
			knownPieces[pCid] = struct{}{}

			if dealID == nil {
				continue
			}

			knownDeals[*dealID] = filDeal{
				pieceCid: pCid,
				status:   *dealStatus,
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()

		knownProviders := make(map[filaddr.Address]struct{}, 4096)

		var newDealCount, terminatedDealCount int
		dealTotals := make(map[string]int64)
		defer func() {
			log.Infow("summary",
				"knownPieces", len(knownPieces),
				"relatedDeals", dealTotals,
				"totalProviders", len(knownProviders),
				"newlyAdded", newDealCount,
				"newlyTerminated", terminatedDealCount,
			)
		}()

		if len(knownPieces) == 0 {
			return nil
		}

		rows, err = ddcommon.Db.Query(
			ctx,
			`SELECT provider FROM discover.providers`,
		)
		if err != nil {
			return err
		}
		for rows.Next() {
			var provStr string
			if err = rows.Scan(&provStr); err != nil {
				return err
			}
			prov, err := filaddr.NewFromString(provStr)
			if err != nil {
				return err
			}
			knownProviders[prov] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()

		if err = <-dealsQueried; err != nil {
			return err
		}

		log.Infof("checking the status of %s known Piece CIDs", humanize.Comma(int64(len(knownPieces))))

		clientLookup := make(map[filaddr.Address]filaddr.Address, 32)
		for dealIDString, d := range stateDeals {
			if _, known := knownPieces[d.Proposal.PieceCID]; !known {
				continue
			}

			dealID, err := strconv.ParseInt(dealIDString, 10, 64)
			if err != nil {
				return err
			}

			labelCid, err := cid.Parse(d.Proposal.Label)
			if err != nil {
				return err
			}

			var initialEncounter bool
			if _, known := knownDeals[dealID]; !known {
				initialEncounter = true
			} else {
				// at the end whatever remains is not in SMA list, thus will be marked "terminated"
				delete(knownDeals, dealID)
			}

			if _, known := knownProviders[d.Proposal.Provider]; !known {
				_, err = ddcommon.Db.Exec(
					ctx,
					`
					INSERT INTO discover.providers ( provider ) VALUES ( $1 )
						ON CONFLICT ( provider ) DO NOTHING
					`,
					d.Proposal.Provider.String(),
				)
				if err != nil {
					return err
				}

				knownProviders[d.Proposal.Provider] = struct{}{}
			}

			if _, known := clientLookup[d.Proposal.Client]; !known {

				robust, err := ddcommon.LotusAPI.StateAccountKey(ctx, d.Proposal.Client, stateTipset.Key())
				if err != nil {
					return err
				}

				clientLookup[d.Proposal.Client] = robust
			}

			var statusMeta *string
			var sectorStart *filabi.ChainEpoch
			status := "published"
			if d.State.SectorStartEpoch > 0 {
				sectorStart = &d.State.SectorStartEpoch
				status = "active"
				m := fmt.Sprintf(
					"containing sector active as of %s at epoch %d",
					ddcommon.MainnetTime(d.State.SectorStartEpoch).Format("2006-01-02 15:04:05"),
					d.State.SectorStartEpoch,
				)
				statusMeta = &m
			} else if d.Proposal.StartEpoch+filprovider.WPoStChallengeWindow < stateTipset.Height() {
				// if things are lookback+one deadlines late: they are never going to make it
				status = "terminated"
				m := fmt.Sprintf(
					"containing sector missed expected sealing epoch %d",
					d.Proposal.StartEpoch,
				)
				statusMeta = &m
			}

			dealTotals[status]++
			if initialEncounter {
				if status == "terminated" {
					terminatedDealCount++
				} else {
					newDealCount++
				}
			}

			if d.Proposal.VerifiedDeal && status == "published" {
				if dcap, knownClient := knownClients[clientLookup[d.Proposal.Client]]; knownClient {
					filbig.Add(*dcap, filbig.NewInt(int64(d.Proposal.PieceSize)))
				}
			}

			var lastStatus *string
			err = ddcommon.Db.QueryRow(
				ctx,
				`
				INSERT INTO discover.published_deals
					( deal_id, piece_cid, label_cid, provider, client, fil_plus, start_epoch, end_epoch, status, status_meta, sector_start_epoch )
					VALUES ( $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11 )
				ON CONFLICT ( deal_id ) DO UPDATE SET
					status = EXCLUDED.status,
					status_meta = EXCLUDED.status_meta,
					sector_start_epoch = COALESCE( EXCLUDED.sector_start_epoch, discover.published_deals.sector_start_epoch )
				RETURNING
					-- this select sees the table as it was before the upsert
					(
						SELECT status
							FROM discover.published_deals resel
						WHERE
							resel.deal_id = published_deals.deal_id
					)
				`,
				dealID,
				d.Proposal.PieceCID.String(),
				ddcommon.CidV1(labelCid).String(),
				d.Proposal.Provider.String(),
				clientLookup[d.Proposal.Client].String(),
				d.Proposal.VerifiedDeal,
				d.Proposal.StartEpoch,
				d.Proposal.EndEpoch,
				status,
				statusMeta,
				sectorStart,
			).Scan(&lastStatus)
			if err != nil {
				return err
			}

			if d.Proposal.VerifiedDeal && status == "active" && (lastStatus == nil || *lastStatus != "active") {
				if _, err := ddcommon.Db.Exec(
					ctx,
					`
					UPDATE discover.proposals
						SET active_deal_id = $1
					WHERE
						proposal_failure = ''
							AND
						proposal_success_cid IS NOT NULL
							AND
						active_deal_id IS NULL
							AND
						piece_cid = $2
							AND
						provider = $3
							AND
						client = $4
					`,
					dealID,
					d.Proposal.PieceCID.String(),
					d.Proposal.Provider.String(),
					clientLookup[d.Proposal.Client].String(),
				); err != nil {
					return err
				}
			}
		}

		// we may have some terminations ( no longer in the market state )
		toFail := make([]int64, 0, len(knownDeals))
		for dID, d := range knownDeals {
			dealTotals["terminated"]++
			if d.status == "terminated" {
				continue
			}
			terminatedDealCount++
			toFail = append(toFail, dID)
		}
		if len(toFail) > 0 {
			_, err = ddcommon.Db.Exec(
				ctx,
				`
				UPDATE discover.published_deals SET
					status = $1,
					status_meta = $2
				WHERE
					deal_id = ANY ( $3::BIGINT[] )
						AND
					status != 'terminated'
				`,
				`terminated`,
				`deal no longer part of market-actor state`,
				toFail,
			)
			if err != nil {
				return err
			}
		}

		for c, d := range knownClients {
			var di *int64
			if d != nil {
				v := d.Int64()
				di = &v
			}
			if _, err = ddcommon.Db.Exec(
				ctx,
				`
				UPDATE discover.clients SET
					non_activated_datacap = $1
				WHERE
					client = $2
				`,
				di,
				c.String(),
			); err != nil {
				return err
			}
		}

		return nil
	},
}
