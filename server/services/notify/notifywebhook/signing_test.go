// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package notifywebhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSignPayloadVectors(t *testing.T) {
	goldenBody, err := os.ReadFile(filepath.Join("testdata", "envelope_golden.json"))
	require.NoError(t, err)

	tests := []struct {
		name      string
		secret    string
		timestamp int64
		body      []byte
		expected  string
	}{
		{
			name:      "simple JSON",
			secret:    "test-secret",
			timestamp: 1784500000000,
			body:      []byte(`{"hello":"world"}`),
			expected:  "sha256=45a00bc67318bd98177f9a80b7055c6f46e810f7d6112bd4005089a5552034b9",
		},
		{
			name:      "empty body",
			secret:    "secret",
			timestamp: 1700000000000,
			body:      []byte{},
			expected:  "sha256=a7494f803b9c6508c0576777e04b19e49501e8f2ef43081ad6d470867ded87f4",
		},
		{
			name:      "golden envelope",
			secret:    "factory-ingress-test-secret",
			timestamp: 1784500000000,
			body:      goldenBody,
			expected:  "sha256=6baa0630f04ccdbbdfa6d0f230261d4a55ae75649339f3bbfc0a73e050a593af",
		},
	}

	// Expected values were computed with OpenSSL
	// (`printf '%s' '<ts>.<body>' | openssl dgst -sha256 -hmac '<secret>'`);
	// these freeze the cross-repo signing contract — changes require
	// coordination with consumers.
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, signPayload(tt.secret, tt.timestamp, tt.body))
		})
	}
}

func TestSignPayloadMaterialShape(t *testing.T) {
	first := signPayload("s", 12, []byte("3x"))
	second := signPayload("s", 123, []byte("x"))

	bodyOnlyMAC := hmac.New(sha256.New, []byte("s"))
	_, err := bodyOnlyMAC.Write([]byte("3x"))
	require.NoError(t, err)
	bodyOnly := signaturePrefix + hex.EncodeToString(bodyOnlyMAC.Sum(nil))

	assert.NotEqual(t, first, second)
	assert.NotEqual(t, first, bodyOnly)
	assert.NotEqual(t, second, bodyOnly)
}
