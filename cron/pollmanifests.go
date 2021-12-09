package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/filecoin-project/filecoin-discover-dealer/ddcommon"
	"github.com/jackc/pgx/v4"
	"github.com/urfave/cli/v2"
)

type manifest struct {
	DriveIdentifier    string
	ValidationStart    time.Time
	ValidationFinish   time.Time
	SoftFailures       int
	HardFailures       int
	Flawless           int
	CarfilesPerDataset map[string]int
	Carfiles           map[string]*carInfo
}

type carInfo struct {
	FullPath  string
	DatasetID int16
	ByteSize  int

	ByteSizeValidated  bool
	CarHeaderValidated bool
	CommpValidated     bool

	SoftFails []string
	HardFails []string
}

var serNoMatch = regexp.MustCompile(`[-_]ST.+?[-_]([A-Za-z0-9]+)$`)

var pollManifests = &cli.Command{
	Usage: "Poll newly uploaded drive mainfests",
	Name:  "poll-manifests",
	Flags: []cli.Flag{},
	Action: func(cctx *cli.Context) error {
		ctx := cctx.Context

		awscfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(cctx.String("manifests_s3_region")))
		if err != nil {
			return err
		}
		s3api := s3.NewFromConfig(awscfg)
		s3Bucket := aws.String(cctx.String("manifests_s3_bucket"))

		mIDextract := regexp.MustCompile(`^manifests/(.+)\.json$`)

		toProcess := make(map[string]time.Time, 8192)
		var total, newRoots int64
		defer func() {
			log.Infow("summary",
				"knownManifests", total,
				"newlyProcessed", len(toProcess),
				"newRoots", newRoots,
			)
		}()

		var nextPage *string
		for {
			ls, err := s3api.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
				Bucket:            s3Bucket,
				ContinuationToken: nextPage,
			})
			if err != nil {
				return err
			}
			for _, e := range ls.Contents {
				if sub := mIDextract.FindStringSubmatch(*e.Key); len(sub) == 2 {
					toProcess[sub[1]] = *e.LastModified
				}
			}
			nextPage = ls.NextContinuationToken
			if nextPage == nil {
				break
			}
		}

		rows, err := ddcommon.Db.Query(
			ctx,
			`SELECT manifest_id, uploaded_at FROM discover.manifests`,
		)
		if err != nil {
			return err
		}
		for rows.Next() {
			var mID string
			var uploadedAt time.Time
			if err = rows.Scan(&mID, &uploadedAt); err != nil {
				return err
			}
			total++
			if t, present := toProcess[mID]; present {
				if !t.Equal(uploadedAt) {
					return fmt.Errorf(
						"unexpected stored timestamp (%s) vs s3 timestamp (%s) for manifest %s",
						t,
						uploadedAt,
						mID,
					)
				}
				delete(toProcess, mID)
			}
		}
		if err = rows.Err(); err != nil {
			return err
		}
		rows.Close()

		if len(toProcess) == 0 {
			return nil
		}
		total += int64(len(toProcess))

		rows, err = ddcommon.Db.Query(
			ctx,
			`SELECT drive_serno FROM discover.drives`,
		)
		if err != nil {
			return err
		}
		knownDrives := make(map[string]struct{}, 2048)
		for rows.Next() {
			var serNo string
			if err = rows.Scan(&serNo); err != nil {
				return err
			}
			knownDrives[serNo] = struct{}{}
		}
		if err = rows.Err(); err != nil {
			return err
		}
		rows.Close()

		driveRows := make([][]interface{}, 0)
		manifestRows := make([][]interface{}, 0)
		manifestEntryRows := make([][]interface{}, 0)

		for mID := range toProcess {
			s3obj, err := s3api.GetObject(ctx, &s3.GetObjectInput{
				Bucket: s3Bucket,
				Key:    aws.String(fmt.Sprintf("manifests/%s.json", mID)),
			})
			if err != nil {
				return err
			}

			var ms manifest
			if err = json.NewDecoder(s3obj.Body).Decode(&ms); err != nil {
				return err
			}

			driveIDJs, err := json.Marshal(ms.DriveIdentifier)
			if err != nil {
				return err
			}

			var serNo *string
			if sm := serNoMatch.FindStringSubmatch(ms.DriveIdentifier); len(sm) == 2 {
				serNo = &sm[1]

				if _, known := knownDrives[*serNo]; !known {
					driveRows = append(driveRows, []interface{}{serNo})
					knownDrives[*serNo] = struct{}{}
				}
			}

			manifestRows = append(manifestRows, []interface{}{
				mID,
				serNo,
				ms.ValidationStart,
				s3obj.LastModified,
				[]byte(fmt.Sprintf(`{ "drive_id" : %s }`, driveIDJs)),
			})

			for cidStr, car := range ms.Carfiles {

				em := struct {
					ValidSize   bool     `json:"valid_size,omitempty"`
					ValidHeader bool     `json:"valid_header,omitempty"`
					ValidCommP  bool     `json:"valid_commp,omitempty"`
					Size        int      `json:"size,omitempty"`
					Failures    []string `json:"failures,omitempty"`
				}{
					ValidSize:   car.ByteSizeValidated,
					ValidHeader: car.CarHeaderValidated,
					ValidCommP:  car.CommpValidated,
					Size:        car.ByteSize,
					Failures:    car.HardFails,
				}

				emJSON, err := json.Marshal(em)
				if err != nil {
					return err
				}

				manifestEntryRows = append(manifestEntryRows, []interface{}{
					mID,
					cidStr,
					car.FullPath,
					emJSON,
				})
			}
		}

		tx, err := ddcommon.Db.Begin(ctx)
		if err != nil {
			return err
		}

		_, err = tx.CopyFrom(
			ctx,
			pgx.Identifier{"discover", "drives"},
			[]string{"drive_serno"},
			pgx.CopyFromRows(driveRows),
		)
		if err != nil {
			return err
		}

		_, err = tx.CopyFrom(
			ctx,
			pgx.Identifier{"discover", "manifests"},
			[]string{"manifest_id", "drive_serno", "validated_at", "uploaded_at", "meta"},
			pgx.CopyFromRows(manifestRows),
		)
		if err != nil {
			return err
		}

		_, err = tx.CopyFrom(
			ctx,
			pgx.Identifier{"discover", "manifest_entries"},
			[]string{"manifest_id", "claimed_root_cid", "local_path", "meta"},
			pgx.CopyFromRows(manifestEntryRows),
		)
		if err != nil {
			return err
		}

		res, err := tx.Exec(
			ctx,
			`
			UPDATE car_files
				SET root_cid = meta->>'dynamo_root'
			WHERE
				root_cid IS NULL
					AND
				meta->>'stable_key' = 'true'
					AND
				( meta->>'dynamo_root', meta->>'payload_size' ) IN (
					SELECT claimed_root_cid, meta->>'size'
						FROM manifest_entries
					WHERE
						( meta->'valid_commp' = 'true' OR meta->'valid_header' = 'true' )
							AND
						meta->'valid_size' = 'true'
				)
			`,
		)
		if err != nil {
			return err
		}
		newRoots = res.RowsAffected()

		return tx.Commit(ctx)
	},
}
