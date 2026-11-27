package controller

import (
	"crypto/hmac"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// SatelliteSync is intentionally outside the browser authentication API.
// Its dedicated per-node credential grants replication, never console login.
func SatelliteSync(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if common.IsSatellite() {
		c.Status(http.StatusNotFound)
		return
	}
	var peers map[string]string
	if err := common.UnmarshalJsonStr(os.Getenv("SATELLITE_SYNC_NODES"), &peers); err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	nodeID := c.GetHeader("X-Satellite-Node")
	secret := peers[nodeID]
	if !service.ValidSatelliteNodeID(nodeID) || len(secret) < 32 {
		c.Status(http.StatusUnauthorized)
		return
	}
	timestamp, nonce := c.GetHeader("X-Satellite-Time"), c.GetHeader("X-Satellite-Nonce")
	stamp, err := strconv.ParseInt(timestamp, 10, 64)
	now := time.Now().Unix()
	if err != nil || stamp < now-90 || stamp > now+90 {
		c.Status(http.StatusUnauthorized)
		return
	}
	decodedNonce, err := hex.DecodeString(nonce)
	if err != nil || len(decodedNonce) != 24 {
		c.Status(http.StatusUnauthorized)
		return
	}
	admission := service.SatelliteSyncSignature(secret, "admission", nodeID, timestamp, nonce, nil)
	if !hmac.Equal([]byte(admission), []byte(c.GetHeader("X-Satellite-Authorization"))) {
		c.Status(http.StatusUnauthorized)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, service.SatelliteSyncBodyLimit)
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.Status(http.StatusRequestEntityTooLarge)
		return
	}
	expected := service.SatelliteSyncSignature(secret, "request", nodeID, timestamp, nonce, body)
	if !hmac.Equal([]byte(expected), []byte(c.GetHeader("X-Satellite-Signature"))) {
		c.Status(http.StatusUnauthorized)
		return
	}
	// Durable nonce consumption applies across primary processes and restarts.
	if err := model.ClaimSatelliteNonce(c.Request.Context(), nodeID, nonce, now); err != nil {
		c.Status(http.StatusConflict)
		return
	}
	var request service.SatelliteSyncRequest
	if err := common.Unmarshal(body, &request); err != nil || request.Version != 1 || len(request.Events) > 64 || len(request.Accounting) > 100000 {
		c.Status(http.StatusBadRequest)
		return
	}
	if err := model.ApplySatelliteAccountingDeltas(c.Request.Context(), nodeID, request.Accounting); err != nil {
		common.SysError("satellite accounting import failed for node " + nodeID)
		c.Status(http.StatusConflict)
		return
	}
	response := service.SatelliteSyncResponse{Version: 1}
	for _, event := range request.Events {
		if err := model.ImportSatelliteLogEvent(c.Request.Context(), nodeID, event); err != nil {
			common.SysError("satellite log import failed for node " + nodeID)
			c.Status(http.StatusConflict)
			return
		}
		response.Acknowledged = append(response.Acknowledged, event.ID)
	}
	response.Config, err = model.ExportSatelliteConfig(c.Request.Context(), nodeID)
	if err != nil {
		common.SysError("satellite configuration export failed for node " + nodeID)
		c.Status(http.StatusInternalServerError)
		return
	}
	responseBody, err := common.Marshal(response)
	if err != nil || int64(len(responseBody)) > service.SatelliteSyncBodyLimit {
		c.Status(http.StatusInternalServerError)
		return
	}
	c.Header("X-Satellite-Signature", service.SatelliteSyncSignature(secret, "response", nodeID, timestamp, nonce, responseBody))
	c.Data(http.StatusOK, "application/json", responseBody)
}
