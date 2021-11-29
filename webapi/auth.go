package main

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strconv"
	"time"

	ddcommon "github.com/filecoin-project/filecoin-discover-dealer/ddcommon"
	filaddr "github.com/filecoin-project/go-address"
	filabi "github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/crypto"
	filcrypto "github.com/filecoin-project/go-state-types/crypto"
	filbuild "github.com/filecoin-project/lotus/build"
	"github.com/filecoin-project/lotus/chain/types"
	filtypes "github.com/filecoin-project/lotus/chain/types"
	lru "github.com/hashicorp/golang-lru"
	"github.com/labstack/echo/v4"
)

const (
	sigGraceEpochs = 3
	authScheme     = `FIL-SPID-V0`
)

type sigChallenge struct {
	authHdr string
	spID    filaddr.Address
	epoch   int64
	hdr     struct {
		epoch   string
		spid    string
		sigType string
		sigB64  string
	}
}

var (
	authRe            = regexp.MustCompile(`^` + authScheme + `\s+([0-9]+)\s*;\s*(f0[0-9]+)\s*;\s*(2)\s*;\s*([^; ]+)\s*$`)
	challengeCache, _ = lru.New(sigGraceEpochs * 128)
	beaconCache, _    = lru.New(sigGraceEpochs * 4)
)

func spidAuth(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {

		var challenge sigChallenge

		challenge.authHdr = c.Request().Header.Get(echo.HeaderAuthorization)
		res := authRe.FindStringSubmatch(challenge.authHdr)
		if len(res) != 5 {
			return authFail(c, "invalid/unexpected FIL-SPID Authorization header '%s'", challenge.authHdr)
		}

		var err error
		challenge.hdr.epoch, challenge.hdr.spid, challenge.hdr.sigType, challenge.hdr.sigB64 = res[1], res[2], res[3], res[4]

		challenge.spID, err = filaddr.NewFromString(challenge.hdr.spid)
		if err != nil {
			return authFail(c, "unexpected FIL-SPID auth address '%s'", challenge.hdr.spid)
		}

		challenge.epoch, err = strconv.ParseInt(challenge.hdr.epoch, 10, 32)
		if err != nil {
			return authFail(c, "unexpected FIL-SPID auth epoch '%s'", challenge.hdr.epoch)
		}

		curFilEpoch := (time.Now().Unix() - int64(ddcommon.FilGenesisUnix)) / 30
		if curFilEpoch < challenge.epoch {
			return authFail(c, "FIL-SPID auth epoch '%d' is in the future", challenge.epoch)
		}
		if curFilEpoch-challenge.epoch > sigGraceEpochs {
			return authFail(c, "FIL-SPID auth epoch '%d' is too far in the past", challenge.epoch)
		}

		var authErr string
		if maybeErr, known := challengeCache.Get(challenge.hdr); known {
			authErr = maybeErr.(string)
		} else {
			authErr, err = verifySig(c, challenge)
			if err != nil {
				return err
			}
			challengeCache.Add(challenge.hdr, authErr)
		}

		if authErr != "" {
			return authFail(c, authErr)
		}

		c.Response().Header().Set("X-FIL-SPID", challenge.spID.String())
		return next(c)
	}
}

func verifySig(c echo.Context, challenge sigChallenge) (string, error) {

	// a worker can only be a BLS key
	if challenge.hdr.sigType != fmt.Sprintf("%d", filcrypto.SigTypeBLS) {
		return fmt.Sprintf("unexpected FIL-SPID auth signature type '%s'", challenge.hdr.sigType), nil
	}

	sig, err := base64.StdEncoding.DecodeString(challenge.hdr.sigB64)
	if err != nil {
		return fmt.Sprintf("unexpected FIL-SPID auth signature encoding '%s'", challenge.hdr.sigB64), nil
	}

	ctx := c.Request().Context()

	var be *filtypes.BeaconEntry
	if protoBe, didFind := beaconCache.Get(challenge.epoch); didFind {
		be = protoBe.(*filtypes.BeaconEntry)
	} else {
		be, err = ddcommon.LotusAPI.BeaconGetEntry(ctx, filabi.ChainEpoch(challenge.epoch))
		if err != nil {
			return "", err
		}
		beaconCache.Add(challenge.epoch, be)
	}

	miFinTs, err := ddcommon.LotusAPI.ChainGetTipSetByHeight(ctx, filabi.ChainEpoch(challenge.epoch)-filbuild.Finality, types.EmptyTSK)
	if err != nil {
		return "", err
	}
	mi, err := ddcommon.LotusAPI.StateMinerInfo(ctx, challenge.spID, miFinTs.Key())
	if err != nil {
		return "", err
	}
	workerAddr, err := ddcommon.LotusAPI.StateAccountKey(ctx, mi.Worker, miFinTs.Key())
	if err != nil {
		return "", err
	}

	sigMatch, err := ddcommon.LotusAPI.WalletVerify(
		ctx,
		workerAddr,
		append([]byte{0x20, 0x20, 0x20}, be.Data...),
		&crypto.Signature{
			Type: 2,
			Data: []byte(sig),
		},
	)
	if err != nil {
		return "", err
	}
	if !sigMatch {
		return fmt.Sprintf("FIL-SPID signature validation failed for auth header '%s'", challenge.authHdr), nil
	}

	return "", nil
}
