package account

import (
	"bytes"

	"github.com/elastos/Elastos.ELA/account"
	"github.com/elastos/Elastos.ELA/core/types"
	"github.com/elastos/Elastos.ELA/core/types/payload"
	"github.com/elastos/Elastos.ELA/crypto"
)

type DposAccount interface {
	SignProposal(proposal *payload.DPOSProposal) ([]byte, error)
	SignVote(vote *payload.DPOSProposalVote) ([]byte, error)
	SignPeerNonce(nonce []byte) (signature [64]byte)
	SignTx(tx *types.Transaction) ([]byte, error)
}

type dposAccount struct {
	*account.Account
}

func (a *dposAccount) SignProposal(proposal *payload.DPOSProposal) ([]byte,
	error) {
	privateKey := a.PrivKey()

	signature, err := crypto.Sign(privateKey, proposal.Data())
	if err != nil {
		return []byte{0}, err
	}

	return signature, nil
}

func (a *dposAccount) SignVote(vote *payload.DPOSProposalVote) ([]byte, error) {
	privateKey := a.PrivKey()

	signature, err := crypto.Sign(privateKey, vote.Data())
	if err != nil {
		return []byte{0}, err
	}

	return signature, nil
}

func (a *dposAccount) SignPeerNonce(nonce []byte) (signature [64]byte) {
	privateKey := a.PrivKey()

	sign, err := crypto.Sign(privateKey, nonce)
	if err != nil || len(signature) != 64 {
		return signature
	}

	copy(signature[:], sign)

	return signature
}

func (a *dposAccount) SignTx(tx *types.Transaction) ([]byte, error) {
	buf := new(bytes.Buffer)
	if err := tx.SerializeUnsigned(buf); err != nil {
		return nil, err
	}

	privateKey := a.PrivKey()
	return crypto.Sign(privateKey, buf.Bytes())
}

func NewDposAccount(password []byte) (DposAccount, error) {
	client, err := account.Open(account.KeystoreFileName, password)
	if err != nil {
		return nil, err
	}
	acc := client.GetMainAccount()

	return &dposAccount{acc}, nil
}

func NewDposAccountFromExisting(a *account.Account) DposAccount {
	return &dposAccount{a}
}
