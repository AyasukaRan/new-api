package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/authz"
)

const SatelliteSyncPath = "/internal/satellite/sync"
const SatelliteSyncBodyLimit int64 = 80 << 20

type SatelliteSyncRequest struct {
	Version    int                              `json:"version"`
	Accounting []model.SatelliteAccountingDelta `json:"accounting"`
	Events     []model.SatelliteLogEvent        `json:"events"`
}

type SatelliteSyncResponse struct {
	Version      int                            `json:"version"`
	Acknowledged []int64                        `json:"acknowledged"`
	Config       *model.SatelliteConfigSnapshot `json:"config"`
}

// Signing covers the endpoint, identity, expiry window, nonce and exact bytes.
// The connection additionally must be TLS or loopback inside the SSH tunnel.
func SatelliteSyncSignature(secret, purpose, nodeID, timestamp, nonce string, body []byte) string {
	hash := sha256.Sum256(body)
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%s\n%s\n%s\n%s\n%s\n%x", purpose, SatelliteSyncPath, nodeID, timestamp, nonce, hash)
	return hex.EncodeToString(mac.Sum(nil))
}

func ValidateSatelliteSyncConfiguration() error {
	if !common.IsSatellite() {
		return nil
	}
	if !ValidSatelliteNodeID(common.SatelliteNodeID()) || len(os.Getenv("SATELLITE_SYNC_SECRET")) < 32 {
		return errors.New("satellite requires a stable node ID and a dedicated sync secret of at least 32 bytes")
	}
	endpoint, err := url.Parse(os.Getenv("SATELLITE_SYNC_URL"))
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") {
		return errors.New("invalid satellite sync URL")
	}
	if endpoint.Scheme != "https" && !(endpoint.Scheme == "http" && (endpoint.Hostname() == "127.0.0.1" || endpoint.Hostname() == "::1")) {
		return errors.New("satellite sync requires HTTPS or a loopback tunnel")
	}
	return nil
}

func ValidSatelliteNodeID(id string) bool {
	if len(id) < 1 || len(id) > 48 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

var satelliteSyncMu sync.Mutex

// SyncSatelliteOnce is also used before accepting the first relay request.
// One worker and one response at a time prevent stale anchor reconciliation.
func SyncSatelliteOnce(ctx context.Context, includeEvents bool) error {
	satelliteSyncMu.Lock()
	defer satelliteSyncMu.Unlock()
	if err := ValidateSatelliteSyncConfiguration(); err != nil {
		return err
	}
	return model.WithSatelliteSyncLock(ctx, func() error {
		return syncSatelliteExchange(ctx, includeEvents)
	})
}

func syncSatelliteExchange(ctx context.Context, includeEvents bool) error {
	request := SatelliteSyncRequest{Version: 1}
	var err error
	request.Accounting, err = model.GetSatelliteAccountingDeltas(ctx)
	if err != nil {
		return err
	}
	if includeEvents {
		request.Events, err = model.PendingSatelliteLogEvents(ctx, 64, 4<<20)
		if err != nil {
			return err
		}
	}
	body, err := common.Marshal(request)
	if err != nil {
		return err
	}
	if int64(len(body)) > SatelliteSyncBodyLimit {
		return errors.New("satellite sync batch exceeds the transfer limit")
	}
	nonceBytes := make([]byte, 24)
	if _, err := rand.Read(nonceBytes); err != nil {
		return err
	}
	nonce := hex.EncodeToString(nonceBytes)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nodeID, secret := common.SatelliteNodeID(), os.Getenv("SATELLITE_SYNC_SECRET")
	endpoint := strings.TrimSuffix(os.Getenv("SATELLITE_SYNC_URL"), "/") + SatelliteSyncPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Satellite-Node", nodeID)
	req.Header.Set("X-Satellite-Time", timestamp)
	req.Header.Set("X-Satellite-Nonce", nonce)
	req.Header.Set("X-Satellite-Authorization", SatelliteSyncSignature(secret, "admission", nodeID, timestamp, nonce, nil))
	req.Header.Set("X-Satellite-Signature", SatelliteSyncSignature(secret, "request", nodeID, timestamp, nonce, body))
	client := &http.Client{Timeout: 50 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("satellite primary connection failed")
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, SatelliteSyncBodyLimit+1))
	if err != nil {
		return errors.New("satellite primary response could not be read")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("satellite primary returned HTTP %d", resp.StatusCode)
	}
	if int64(len(responseBody)) > SatelliteSyncBodyLimit {
		return errors.New("satellite primary response exceeds limit")
	}
	expected := SatelliteSyncSignature(secret, "response", nodeID, timestamp, nonce, responseBody)
	if !hmac.Equal([]byte(expected), []byte(resp.Header.Get("X-Satellite-Signature"))) {
		return errors.New("satellite primary response authentication failed")
	}
	var result SatelliteSyncResponse
	if err := common.Unmarshal(responseBody, &result); err != nil {
		return errors.New("invalid satellite primary response")
	}
	if result.Version != 1 || result.Config == nil {
		return errors.New("unsupported satellite primary response")
	}
	_, err = model.ApplySatelliteConfig(ctx, result.Config)
	if err != nil {
		return errors.New("satellite configuration could not be committed")
	}
	if includeEvents {
		// Only IDs sent by this worker may be acknowledged by the peer.
		sent := make(map[int64]bool, len(request.Events))
		for _, event := range request.Events {
			sent[event.ID] = true
		}
		for _, id := range result.Acknowledged {
			if !sent[id] {
				return errors.New("invalid satellite event acknowledgement")
			}
		}
		if err := model.AcknowledgeSatelliteLogEvents(ctx, result.Acknowledged); err != nil {
			return err
		}
	}
	if includeEvents {
		if err := model.LoadOptionsFromDatabase(); err != nil {
			return err
		}
		model.InitChannelCache()
		model.RefreshPricing()
		if err := model.PurgeSubscriptionPlanCache(); err != nil {
			return err
		}
		if err := authz.ReloadPolicy(); err != nil {
			return err
		}
	}
	return nil
}

func StartSatelliteSync(ctx context.Context) {
	if !common.IsSatellite() {
		return
	}
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			// Continue bounded batches promptly when catching up after an outage.
			for range 16 {
				batchCtx, cancel := context.WithTimeout(ctx, 55*time.Second)
				err := SyncSatelliteOnce(batchCtx, true)
				cancel()
				if err != nil {
					common.SysError("satellite synchronization failed: " + err.Error())
					break
				}
				pending, err := model.PendingSatelliteLogEvents(ctx, 1, 1)
				if err != nil || len(pending) == 0 {
					break
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
