// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package notifywebhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

const (
	// HeaderSignature carries "sha256=" + lowercase hex
	// HMAC-SHA256(secret, timestamp + "." + body). Sent only when a
	// signing secret is configured.
	HeaderSignature = "X-Boards-Webhook-Signature"

	// HeaderTimestamp carries the delivery attempt time in milliseconds
	// since epoch, as a decimal string. It is the same string that
	// prefixes the signed material, bounding replay. Always sent.
	HeaderTimestamp = "X-Boards-Webhook-Timestamp"

	signaturePrefix = "sha256="
)

// signPayload computes the signature header value for one delivery
// attempt. The signed material is the decimal timestamp, a literal
// period, and the raw request body bytes — this tuple is a frozen
// cross-repo contract; never change the concatenation.
func signPayload(secret string, timestampMillis int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(timestampMillis, 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	return signaturePrefix + hex.EncodeToString(mac.Sum(nil))
}
