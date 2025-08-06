package protocols

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/infrared-dao/protocols/fetchers"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"
)

/*
Pendle contracts are very complicated so it is far easier to use their V2 API than onchain methods
The LP token must be wrapped before being inserted to the POL vault, wrapped LP token is the address
Onchain Pendle pool positions are broken into SY, PT, and TY tokens which is the complexity to avoid

We can make 100 calls to the current pool info API endpoint per minute
With 15 sec price updates on prod+staging, we can have ~12 Pendle LP tokens being tracked before limit
Note: because we are only using the current info endpoint, we can't look at a previous specific block
*/

// assuming we only will track Pendle assets on berachain ie. chain id 80094
const (
	pendleV2API = "https://api-v2.pendle.finance/core/v2/80094/markets/%s/data"
)

var _ Protocol = &PendleLPPriceProvider{}

type PendleConfig struct{}

// PendleLPPriceProvider defines the provider for Pendle wrapped LP price and TVL.
type PendleLPPriceProvider struct {
	address     common.Address
	logger      zerolog.Logger
	params      fetchers.HTTPParams
	cacheResult PendlePoolCurrentState
	cacheTime   time.Time
}

// NewPendleLPPriceProvider creates a new instance of the PendleLPPriceProvider.
func NewPendleLPPriceProvider(
	address common.Address,
	logger zerolog.Logger,
	config []byte,
) *PendleLPPriceProvider {
	endpoint := fmt.Sprintf(pendleV2API, strings.ToLower(address.Hex()))
	params := fetchers.HTTPParams{
		URL: endpoint,
		Headers: map[string]string{
			"Content-Type": "application/json; charset=UTF-8",
			"Accept":       "application/json",
		},
		MaxWait: fetchers.DefaultRequestTimeout,
	}

	p := &PendleLPPriceProvider{
		address: address,
		logger:  logger,
		params:  params,
	}
	return p
}

// Initialize creates the web client used to make API calls
func (p *PendleLPPriceProvider) Initialize(ctx context.Context, client *ethclient.Client) error {
	return nil
}

func (p *PendleLPPriceProvider) LPTokenPrice(ctx context.Context) (string, error) {
	supply, tvl, err := p.getSupplyAndTVL(ctx)
	if err != nil {
		return "", err
	}

	if supply.Cmp(decimal.Zero) == 0 {
		err = fmt.Errorf("total supply is zero")
		p.logger.Error().Err(err).Msg("failed to fetch total supply and tvl")
		return "", err
	}

	price := tvl.Div(supply)

	p.logger.Debug().
		Str("totalValue", tvl.String()).
		Str("totalSupply", supply.String()).
		Str("pricePerToken", price.String()).
		Msg("LP token price calculated successfully")

	return price.StringFixed(roundingDecimals), nil
}

func (p *PendleLPPriceProvider) TVL(ctx context.Context) (string, error) {
	_, tvl, err := p.getSupplyAndTVL(ctx)
	if err != nil {
		return "", err
	}

	p.logger.Debug().Str("tvl", tvl.String()).Msg("successfully fetched TVL")
	return tvl.StringFixed(roundingDecimals), nil
}

func (p *PendleLPPriceProvider) GetConfig(ctx context.Context, address string, ethClient *ethclient.Client) ([]byte, error) {
	var err error
	if !common.IsHexAddress(address) {
		err = fmt.Errorf("invalid smart contract address, '%s'", address)
		return nil, err
	}

	pc := &PendleConfig{}
	body, err := json.Marshal(pc)
	if err != nil {
		return nil, err
	}

	return body, nil
}

func (p *PendleLPPriceProvider) UpdateBlock(block *big.Int, prices map[string]Price) {}

// Internal Helper methods not able to be called except in this file

type PendlePoolCurrentState struct {
	Liquidity struct {
		USD float64 `json:"usd"`
	} `json:"liquidity"`
	Supply float64 `json:"totalLp"`
}

// tvl fetches the TVL from the Pendle smart contract.
func (p *PendleLPPriceProvider) getSupplyAndTVL(ctx context.Context) (decimal.Decimal, decimal.Decimal, error) {
	var results PendlePoolCurrentState

	now := time.Now()
	secSinceLast := now.Sub(p.cacheTime).Seconds()

	// only make new API call if it has been at least 5 sec since previous call
	if secSinceLast <= 5.0 {
		results = p.cacheResult
		p.logger.Debug().Msg("Getting value from cache because last call was recent")
	} else {
		responseJSON, err := fetchers.HTTPGet(ctx, p.params)
		if err != nil {
			err = fmt.Errorf("failed to fetch current Pendle Pool state data, %w", err)
			p.logger.Error().Err(err).Msg("Unable to HTTP get the API endpoint")
			return decimal.Zero, decimal.Zero, err
		}

		err = json.Unmarshal(responseJSON, &results)
		if err != nil {
			return decimal.Zero, decimal.Zero, err
		}
		p.cacheResult = results
		p.cacheTime = now
	}

	tvl := decimal.NewFromFloat(results.Liquidity.USD)
	supply := decimal.NewFromFloat(results.Supply)

	return supply, tvl, nil
}
