package soroban

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/stellar/go/keypair"
	rpc "github.com/stellar/go/protocols/rpc"
	"github.com/stellar/go/txnbuild"
	"github.com/stellar/go/xdr"
)

// InvokeResult contains the result of a contract invocation.
type InvokeResult struct {
	TxHash        string
	ResultXDR     string
	ResultMetaXDR string
}

// ReturnValueFromMetaXDR extracts the contract return value (ScVal) from a
// transaction's ResultMetaXDR (TransactionMeta → SorobanMeta → ReturnValue).
// Invoke's ResultXDR is the TransactionResult, not the return value — use this
// to read what a contract method actually returned.
func ReturnValueFromMetaXDR(resultMetaXDR string) (xdr.ScVal, error) {
	var meta xdr.TransactionMeta
	if err := xdr.SafeUnmarshalBase64(resultMetaXDR, &meta); err != nil {
		return xdr.ScVal{}, fmt.Errorf("decode result meta: %w", err)
	}
	if v3, ok := meta.GetV3(); ok {
		if v3.SorobanMeta == nil {
			return xdr.ScVal{}, fmt.Errorf("no soroban meta in transaction result")
		}
		return v3.SorobanMeta.ReturnValue, nil
	}
	if v4, ok := meta.GetV4(); ok {
		if v4.SorobanMeta == nil || v4.SorobanMeta.ReturnValue == nil {
			return xdr.ScVal{}, fmt.Errorf("no soroban return value in transaction result")
		}
		return *v4.SorobanMeta.ReturnValue, nil
	}
	return xdr.ScVal{}, fmt.Errorf("unsupported transaction meta version %d", meta.V)
}

// TransactionResultError decodes a failed transaction's ResultXDR into a
// human-readable error (result code + per-operation result codes).
func TransactionResultError(resultXDR string) error {
	var res xdr.TransactionResult
	if err := xdr.SafeUnmarshalBase64(resultXDR, &res); err != nil {
		return fmt.Errorf("transaction failed on-chain (undecodable result: %v)", err)
	}
	if opRes, ok := res.Result.GetResults(); ok {
		codes := make([]string, 0, len(opRes))
		for _, r := range opRes {
			if tr, ok := r.GetTr(); ok {
				if ihr, ok := tr.GetInvokeHostFunctionResult(); ok {
					codes = append(codes, ihr.Code.String())
					continue
				}
				codes = append(codes, tr.Type.String())
			} else {
				codes = append(codes, r.Code.String())
			}
		}
		return fmt.Errorf("transaction failed on-chain: %s (ops: %v)", res.Result.Code, codes)
	}
	return fmt.Errorf("transaction failed on-chain: %s", res.Result.Code)
}

// DiagnosticErrorFromEventsXDR decodes a transaction's diagnostic event XDR
// strings and returns a readable dump of every event (type + topics + data),
// with error ScVals highlighted. Returns "" if no events are present.
func DiagnosticErrorFromEventsXDR(eventsXDR []string) string {
	var lines []string
	for _, evB64 := range eventsXDR {
		var d xdr.DiagnosticEvent
		if err := xdr.SafeUnmarshalBase64(evB64, &d); err != nil {
			continue
		}
		v0 := d.Event.Body.V0
		if v0 == nil {
			continue
		}
		parts := make([]string, 0, len(v0.Topics)+1)
		for _, t := range v0.Topics {
			parts = append(parts, formatScVal(t))
		}
		// Skip noisy core_metrics events — they only carry metering counters.
		if len(parts) > 0 && parts[0] == "core_metrics" {
			continue
		}
		line := fmt.Sprintf("%s[%s] data=%s", d.Event.Type, strings.Join(parts, ","), formatScVal(v0.Data))
		lines = append(lines, line)
	}
	return strings.Join(lines, " | ")
}

func formatScVal(sv xdr.ScVal) string {
	switch sv.Type {
	case xdr.ScValTypeScvSymbol:
		if s, ok := sv.GetSym(); ok {
			return string(s)
		}
	case xdr.ScValTypeScvString:
		if s, ok := sv.GetStr(); ok {
			return string(s)
		}
	case xdr.ScValTypeScvU32:
		if v, ok := sv.GetU32(); ok {
			return fmt.Sprintf("%d", v)
		}
	case xdr.ScValTypeScvU64:
		if v, ok := sv.GetU64(); ok {
			return fmt.Sprintf("%d", v)
		}
	case xdr.ScValTypeScvBool:
		if v, ok := sv.GetB(); ok {
			return fmt.Sprintf("%v", v)
		}
	case xdr.ScValTypeScvError:
		if e, ok := sv.GetError(); ok {
			return formatScError(&e)
		}
	case xdr.ScValTypeScvAddress:
		if a, ok := sv.GetAddress(); ok {
			if acc, ok := a.GetAccountId(); ok {
				return acc.Address()
			}
			if c, ok := a.GetContractId(); ok {
				return fmt.Sprintf("C%s", hex.EncodeToString(c[:]))
			}
		}
	case xdr.ScValTypeScvVec:
		if v, ok := sv.GetVec(); ok {
			parts := make([]string, 0, len(*v))
			for _, item := range *v {
				parts = append(parts, formatScVal(item))
			}
			return "[" + strings.Join(parts, ",") + "]"
		}
	case xdr.ScValTypeScvMap:
		if m, ok := sv.GetMap(); ok {
			parts := make([]string, 0, len(*m))
			for _, e := range *m {
				parts = append(parts, formatScVal(e.Key)+":"+formatScVal(e.Val))
			}
			return "{" + strings.Join(parts, ",") + "}"
		}
	}
	return sv.Type.String()
}

func formatScError(e *xdr.ScError) string {
	if e.Type == xdr.ScErrorTypeSceContract && e.ContractCode != nil {
		return fmt.Sprintf("Error(Contract, #%d)", *e.ContractCode)
	}
	if e.Code != nil {
		return fmt.Sprintf("Error(%s, %s)", e.Type, *e.Code)
	}
	return fmt.Sprintf("Error(%s)", e.Type)
}

// Invoke submits a contract method call transaction to the network.
func (c *Client) Invoke(
	ctx context.Context,
	kp *keypair.Full,
	contractID string,
	method string,
	args []xdr.ScVal,
) (*InvokeResult, error) {
	contractAddr, err := ParseContractID(contractID)
	if err != nil {
		return nil, fmt.Errorf("invalid contract ID: %w", err)
	}

	sourceAccount, err := c.LoadAccount(ctx, kp.Address())
	if err != nil {
		return nil, fmt.Errorf("failed to load source account: %w", err)
	}

	op := &txnbuild.InvokeHostFunction{
		HostFunction: xdr.HostFunction{
			Type: xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
			InvokeContract: &xdr.InvokeContractArgs{
				ContractAddress: contractAddr,
				FunctionName:    xdr.ScSymbol(method),
				Args:            args,
			},
		},
		SourceAccount: kp.Address(),
	}

	txHash, resultXDR, resultMetaXDR, err := c.simulateAndAssembleAndSubmit(ctx, sourceAccount, op, kp, 0)
	if err != nil {
		return nil, err
	}

	return &InvokeResult{
		TxHash:        txHash,
		ResultXDR:     resultXDR,
		ResultMetaXDR: resultMetaXDR,
	}, nil
}

// SimulateOnly runs a read-only contract method call via simulation (no transaction submitted).
func (c *Client) SimulateOnly(
	ctx context.Context,
	contractID string,
	method string,
	args []xdr.ScVal,
) (returnValueXDR string, err error) {
	contractAddr, err := ParseContractID(contractID)
	if err != nil {
		return "", fmt.Errorf("invalid contract ID: %w", err)
	}

	op := &txnbuild.InvokeHostFunction{
		HostFunction: xdr.HostFunction{
			Type: xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
			InvokeContract: &xdr.InvokeContractArgs{
				ContractAddress: contractAddr,
				FunctionName:    xdr.ScSymbol(method),
				Args:            args,
			},
		},
	}

	dummyKP, _ := keypair.Random()

	tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        &txnbuild.SimpleAccount{AccountID: dummyKP.Address(), Sequence: 0},
		IncrementSequenceNum: true,
		BaseFee:              defaultBaseFee,
		Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(300)},
		Operations:           []txnbuild.Operation{op},
	})
	if err != nil {
		return "", fmt.Errorf("failed to build transaction: %w", err)
	}

	txB64, err := tx.Base64()
	if err != nil {
		return "", fmt.Errorf("failed to serialize transaction: %w", err)
	}

	simResp, err := c.rpc.SimulateTransaction(ctx, rpc.SimulateTransactionRequest{
		Transaction: txB64,
	})
	if err != nil {
		return "", fmt.Errorf("simulation failed: %w", err)
	}
	if simResp.Error != "" {
		return "", fmt.Errorf("simulation error: %s", simResp.Error)
	}

	if len(simResp.Results) == 0 {
		return "", fmt.Errorf("no simulation results returned")
	}

	if simResp.Results[0].ReturnValueXDR == nil {
		return "", fmt.Errorf("no return value in simulation result")
	}

	return *simResp.Results[0].ReturnValueXDR, nil
}
