package market

import (
	"fmt"
	"strings"

	"github.com/ogtechnologies/mozartpay/internal/models"
	"github.com/ogtechnologies/mozartpay/internal/swap"
	"github.com/stellar/go/clients/horizonclient"
	hProtocol "github.com/stellar/go/protocols/horizon"
	"github.com/stellar/go/txnbuild"
)

// ParseAsset resolves an asset from "CODE", "CODE:ISSUER", "XLM", or "native".
func ParseAsset(input string, network models.Network) (models.AssetRef, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return models.AssetRef{}, fmt.Errorf("asset cannot be empty")
	}

	if strings.EqualFold(input, "native") || strings.EqualFold(input, "XLM") {
		return models.AssetRef{Code: "XLM"}, nil
	}

	parts := strings.Split(input, ":")
	if len(parts) > 2 {
		return models.AssetRef{}, fmt.Errorf("invalid asset %q", input)
	}

	code := strings.ToUpper(strings.TrimSpace(parts[0]))
	if len(code) == 0 || len(code) > 12 {
		return models.AssetRef{}, fmt.Errorf("invalid asset code %q", code)
	}

	if len(parts) == 2 {
		issuer := strings.TrimSpace(parts[1])
		if issuer == "" {
			return models.AssetRef{}, fmt.Errorf("asset %q requires an issuer", input)
		}
		return models.AssetRef{Code: code, Issuer: issuer}, nil
	}

	var known map[string]swap.AssetConfig
	if network == models.NetworkStellarMainnet {
		known = swap.MainnetAssets
	} else {
		known = swap.TestnetAssets
	}
	cfg, ok := known[code]
	if !ok {
		return models.AssetRef{}, fmt.Errorf("unknown asset %q; use CODE:ISSUER", code)
	}
	return models.AssetRef{Code: cfg.Code, Issuer: cfg.Issuer}, nil
}

// Equal compares two assets, treating XLM and native as equivalent.
func Equal(a, b models.AssetRef) bool {
	return canonical(a) == canonical(b)
}

func canonical(a models.AssetRef) string {
	code := strings.ToUpper(strings.TrimSpace(a.Code))
	if code == "" || code == "NATIVE" || code == "XLM" {
		return "native"
	}
	return code + ":" + a.Issuer
}

// DisplayCode returns a compact asset label.
func DisplayCode(a models.AssetRef) string {
	if strings.EqualFold(a.Code, "native") {
		return "XLM"
	}
	return strings.ToUpper(a.Code)
}

// TxnAsset converts an asset reference to a transaction asset.
func TxnAsset(a models.AssetRef) (txnbuild.Asset, error) {
	if canonical(a) == "native" {
		return txnbuild.NativeAsset{}, nil
	}
	if a.Issuer == "" {
		return nil, fmt.Errorf("asset %s requires an issuer", a.Code)
	}
	return txnbuild.CreditAsset{Code: a.Code, Issuer: a.Issuer}, nil
}

// HorizonAsset converts an asset reference to Horizon request fields.
func HorizonAsset(a models.AssetRef) (horizonclient.AssetType, string, string) {
	if canonical(a) == "native" {
		return horizonclient.AssetTypeNative, "", ""
	}
	if len(a.Code) <= 4 {
		return horizonclient.AssetType4, a.Code, a.Issuer
	}
	return horizonclient.AssetType12, a.Code, a.Issuer
}

// FromHorizonAsset converts a Horizon asset to the local representation.
func FromHorizonAsset(a hProtocol.Asset) models.AssetRef {
	if a.Type == "native" {
		return models.AssetRef{Code: "XLM"}
	}
	return models.AssetRef{Code: a.Code, Issuer: a.Issuer}
}
