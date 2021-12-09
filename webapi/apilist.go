package main

import (
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/filecoin-project/filecoin-discover-dealer/ddcommon"
	filaddr "github.com/filecoin-project/go-address"
	"github.com/labstack/echo/v4"
)

func apiProposalsList(c echo.Context) error {

	sp, err := filaddr.NewFromString(c.Response().Header().Get("X-FIL-SPID"))
	if err != nil {
		return err
	}

	// if sp.String() == "f016309" {
	// 	sp, _ = filaddr.NewFromString("f099")
	// }

	ctx := c.Request().Context()

	rows, err := ddcommon.Db.Query(
		ctx,
		`
		SELECT
				p.proposal_success_cid,
				p.start_time,
				c.root_cid,
				c.piece_cid,
				c.padded_piece_size,
				me.manifest_id,
				me.local_path
			FROM discover.proposals p
			JOIN discover.car_files c USING ( piece_cid )
			JOIN discover.manifest_entries me
				ON
					me.manifest_id = p.manifest_id
						AND
					me.claimed_root_cid = c.root_cid
		WHERE
			p.provider = $1
				AND
			p.active_deal_id is NULL
				AND
			(
				p.proposal_success_cid IS NOT NULL
					OR
				p.proposal_failure = ''
			)
		`,
		sp.String(),
	)
	if err != nil {
		return err
	}

	var totalSize int64
	var countPendingProposals int
	type pendingProposal struct {
		pieceSize    int64
		startTime    time.Time
		dealCid      string
		pieceCid     string
		rootCid      string
		manifestID   string
		manifestPath string
	}
	proposals := make([]pendingProposal, 0, 128)
	for rows.Next() {
		var prop pendingProposal
		var dCid *string
		if err = rows.Scan(&dCid, &prop.startTime, &prop.rootCid, &prop.pieceCid, &prop.pieceSize, &prop.manifestID, &prop.manifestPath); err != nil {
			return err
		}
		totalSize += prop.pieceSize
		if dCid == nil {
			countPendingProposals++
		} else {
			prop.dealCid = *dCid
			proposals = append(proposals, prop)
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	rows.Close()

	out := fmt.Sprintf(
		`
#
# There are currently %d deals totalling %.02f GiB queued
# for storage provider %s
#
`,
		len(proposals)+countPendingProposals,
		float64(totalSize)/(1<<30),
		sp.String(),
	)

	if countPendingProposals > 0 {
		out += fmt.Sprintf("# Out of those, %d deals are still in the process of being proposed\n# Please check back in a few minutes\n#\n", countPendingProposals)
	}

	if len(proposals) == 0 {
		return c.String(
			http.StatusOK,
			out,
		)
	}

	out += `
#
# You can run the commands below as-is against your markets node.
# Before this you can optionally set the IMPORT_ROOT and IMPORT_SETTLE_SECONDS
# environment variables to make things easier on your system, although they
# have reasonable defaults.
# ( if you would like this list in JSON format - let us know )
#
`
	sort.Slice(proposals, func(i, j int) bool {
		pi, pj := proposals[i], proposals[j]
		ti, tj := time.Until(pi.startTime).Truncate(time.Hour), time.Until(pj.startTime).Truncate(time.Hour)
		switch {
		case ti != tj:
			return ti < tj
		case pi.manifestID != pj.manifestID:
			return pi.manifestID < pj.manifestID
		default:
			return pi.manifestPath != pj.manifestPath
		}
	})
	for _, p := range proposals {
		out += fmt.Sprintf(
			`
#
#  Source:%s  PieceSize:%.02fGiB  PieceCid:%s  RootCid:%s
#  SealBy:%s  (%.1f hours from now)
lotus-miner storage-deals import-data %s ${IMPORT_ROOT:-.}/%s
sleep ${IMPORT_SETTLE_SECONDS:-2}
`,
			p.manifestID,
			float64(p.pieceSize)/(1<<30),
			p.pieceCid,
			p.rootCid,
			p.startTime.Format("2006-01-02 15:04:05"),
			time.Until(p.startTime).Hours(),
			p.dealCid,
			p.manifestPath,
		)
	}

	return c.String(
		http.StatusOK,
		out,
	)
}
