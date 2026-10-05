package trading

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/ogtechnologies/mozartpay/internal/config"
	"github.com/ogtechnologies/mozartpay/internal/models"
)

// Store persists trading strategies, order intents, executions, and runtime state.
type Store struct {
	db *sql.DB
}

// NewStore opens the default trading database.
func NewStore() (*Store, error) {
	dir, err := config.ConfigDir()
	if err != nil {
		return nil, err
	}
	return NewStoreAt(filepath.Join(dir, "trading.db"))
}

// NewStoreAt opens a trading database at a specific path.
func NewStoreAt(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err := store.init(); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) init() error {
	_, err := s.db.Exec(`
PRAGMA journal_mode=WAL;
CREATE TABLE IF NOT EXISTS strategies (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	type TEXT NOT NULL,
	description TEXT,
	status TEXT NOT NULL,
	network TEXT NOT NULL,
	wallet TEXT NOT NULL DEFAULT '',
	base_asset TEXT NOT NULL,
	quote_asset TEXT NOT NULL,
	parameters_json TEXT NOT NULL,
	risk_limits_json TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	last_run_at TEXT,
	total_trades INTEGER DEFAULT 0,
	total_profit REAL DEFAULT 0,
	active_since TEXT
);
CREATE INDEX IF NOT EXISTS idx_strategies_status ON strategies(status);
CREATE INDEX IF NOT EXISTS idx_strategies_network ON strategies(network);

CREATE TABLE IF NOT EXISTS executions (
	id TEXT PRIMARY KEY,
	strategy_id TEXT NOT NULL,
	strategy_type TEXT NOT NULL,
	timestamp TEXT NOT NULL,
	action TEXT NOT NULL,
	base_asset TEXT NOT NULL,
	quote_asset TEXT NOT NULL,
	amount REAL,
	price REAL,
	value REAL,
	profit_loss REAL,
	profit_pct REAL,
	tx_hash TEXT,
	status TEXT NOT NULL,
	error TEXT,
	metadata_json TEXT
);
CREATE INDEX IF NOT EXISTS idx_executions_strategy ON executions(strategy_id, timestamp);

CREATE TABLE IF NOT EXISTS performance (
	strategy_id TEXT PRIMARY KEY,
	performance_json TEXT NOT NULL,
	updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS managed_offers (
	strategy_id TEXT NOT NULL,
	intent_id TEXT NOT NULL,
	side TEXT NOT NULL,
	base_asset_json TEXT NOT NULL,
	quote_asset_json TEXT NOT NULL,
	price REAL NOT NULL,
	amount REAL NOT NULL,
	passive BOOLEAN DEFAULT 0,
	status TEXT NOT NULL,
	offer_id INTEGER DEFAULT 0,
	tx_hash TEXT,
	last_error TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	submitted_at TEXT,
	PRIMARY KEY (strategy_id, intent_id)
);
CREATE INDEX IF NOT EXISTS idx_managed_offers_offer ON managed_offers(offer_id);
CREATE INDEX IF NOT EXISTS idx_managed_offers_status ON managed_offers(strategy_id, status);

CREATE TABLE IF NOT EXISTS runtime (
	strategy_id TEXT PRIMARY KEY,
	pid INTEGER,
	started_at TEXT,
	last_heartbeat TEXT,
	last_error TEXT,
	consecutive_errors INTEGER DEFAULT 0,
	cycles INTEGER DEFAULT 0,
	dry_run BOOLEAN DEFAULT 1,
	kill_switch BOOLEAN DEFAULT 0
);

CREATE TABLE IF NOT EXISTS fills (
	id TEXT PRIMARY KEY,
	strategy_id TEXT NOT NULL,
	intent_id TEXT,
	offer_id INTEGER,
	trade_id TEXT NOT NULL,
	price REAL,
	amount REAL,
	counter REAL,
	tx_hash TEXT,
	executed_at TEXT NOT NULL,
	UNIQUE(strategy_id, trade_id)
);
CREATE INDEX IF NOT EXISTS idx_fills_strategy ON fills(strategy_id, executed_at);

CREATE TABLE IF NOT EXISTS backtests (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	strategy_id TEXT,
	strategy_name TEXT,
	base_asset TEXT,
	quote_asset TEXT,
	network TEXT,
	iterations INTEGER,
	desired_orders INTEGER,
	buys INTEGER,
	sells INTEGER,
	trades INTEGER,
	realized_pnl REAL,
	ending_value REAL,
	started_at TEXT,
	ended_at TEXT,
	created_at TEXT
);

CREATE TABLE IF NOT EXISTS kv_state (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`)
	if err != nil {
		return err
	}
	return s.migrate()
}

// migrate applies incremental schema changes for databases created by older
// versions (the CREATE TABLE statements above are IF NOT EXISTS and do not
// add columns to existing tables).
func (s *Store) migrate() error {
	var hasWallet bool
	rows, err := s.db.Query(`PRAGMA table_info(strategies)`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var dflt interface{}
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == "wallet" {
			hasWallet = true
		}
	}
	rows.Close()
	if !hasWallet {
		if _, err := s.db.Exec(`ALTER TABLE strategies ADD COLUMN wallet TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	return nil
}

// Close closes the database.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// SaveStrategy creates or updates a strategy.
func (s *Store) SaveStrategy(strategy *models.TradingStrategy) error {
	params, err := json.Marshal(strategy.Parameters)
	if err != nil {
		return err
	}
	risk, err := json.Marshal(strategy.RiskLimits)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO strategies
		(id,name,type,description,status,network,wallet,base_asset,quote_asset,parameters_json,risk_limits_json,created_at,updated_at,last_run_at,total_trades,total_profit,active_since)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
		name=excluded.name,type=excluded.type,description=excluded.description,status=excluded.status,network=excluded.network,
		wallet=excluded.wallet,
		base_asset=excluded.base_asset,quote_asset=excluded.quote_asset,parameters_json=excluded.parameters_json,
		risk_limits_json=excluded.risk_limits_json,updated_at=excluded.updated_at,last_run_at=excluded.last_run_at,
		total_trades=excluded.total_trades,total_profit=excluded.total_profit,active_since=excluded.active_since`,
		strategy.ID, strategy.Name, string(strategy.Type), strategy.Description, string(strategy.Status), string(strategy.Network),
		strategy.Wallet, strategy.BaseAsset, strategy.QuoteAsset, string(params), string(risk), formatTime(strategy.CreatedAt), formatTime(strategy.UpdatedAt),
		timePtrString(strategy.LastRunAt), strategy.TotalTrades, strategy.TotalProfit, timePtrString(strategy.ActiveSince))
	return err
}

// GetStrategy loads a strategy by ID.
func (s *Store) GetStrategy(id string) (*models.TradingStrategy, error) {
	row := s.db.QueryRow(`SELECT id,name,type,description,status,network,wallet,base_asset,quote_asset,parameters_json,risk_limits_json,
		created_at,updated_at,last_run_at,total_trades,total_profit,active_since FROM strategies WHERE id=?`, id)
	return scanStrategy(row)
}

// ListStrategies returns all persisted strategies.
func (s *Store) ListStrategies() ([]*models.TradingStrategy, error) {
	rows, err := s.db.Query(`SELECT id,name,type,description,status,network,wallet,base_asset,quote_asset,parameters_json,risk_limits_json,
		created_at,updated_at,last_run_at,total_trades,total_profit,active_since FROM strategies ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var strategies []*models.TradingStrategy
	for rows.Next() {
		strategy, err := scanStrategy(rows)
		if err != nil {
			return nil, err
		}
		strategies = append(strategies, strategy)
	}
	return strategies, rows.Err()
}

// DeleteStrategy deletes a non-running strategy and associated records.
func (s *Store) DeleteStrategy(id string) error {
	strategy, err := s.GetStrategy(id)
	if err != nil {
		return err
	}
	if strategy.Status == models.StrategyActive {
		return fmt.Errorf("cannot delete active strategy, stop it first")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, query := range []string{
		`DELETE FROM managed_offers WHERE strategy_id=?`,
		`DELETE FROM executions WHERE strategy_id=?`,
		`DELETE FROM fills WHERE strategy_id=?`,
		`DELETE FROM runtime WHERE strategy_id=?`,
		`DELETE FROM performance WHERE strategy_id=?`,
		`DELETE FROM backtests WHERE strategy_id=?`,
		`DELETE FROM strategies WHERE id=?`,
	} {
		if _, err := tx.Exec(query, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SaveExecution persists an execution.
func (s *Store) SaveExecution(exec *models.StrategyExecution) error {
	metadata, _ := json.Marshal(exec.Metadata)
	_, err := s.db.Exec(`INSERT OR REPLACE INTO executions
		(id,strategy_id,strategy_type,timestamp,action,base_asset,quote_asset,amount,price,value,profit_loss,profit_pct,tx_hash,status,error,metadata_json)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		exec.ID, exec.StrategyID, string(exec.StrategyType), formatTime(exec.Timestamp), string(exec.Action), exec.BaseAsset,
		exec.QuoteAsset, exec.Amount, exec.Price, exec.Value, exec.ProfitLoss, exec.ProfitPct, exec.TxHash,
		string(exec.Status), exec.Error, string(metadata))
	return err
}

// ListExecutions returns latest executions for a strategy.
func (s *Store) ListExecutions(strategyID string, limit int) ([]models.StrategyExecution, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT id,strategy_id,strategy_type,timestamp,action,base_asset,quote_asset,amount,price,value,
		profit_loss,profit_pct,tx_hash,status,error,metadata_json FROM executions WHERE strategy_id=? ORDER BY timestamp DESC LIMIT ?`, strategyID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.StrategyExecution
	for rows.Next() {
		var e models.StrategyExecution
		var ts, status, action, strategyType string
		var metadata string
		if err := rows.Scan(&e.ID, &e.StrategyID, &strategyType, &ts, &action, &e.BaseAsset, &e.QuoteAsset,
			&e.Amount, &e.Price, &e.Value, &e.ProfitLoss, &e.ProfitPct, &e.TxHash, &status, &e.Error, &metadata); err != nil {
			return nil, err
		}
		e.Timestamp = parseTime(ts)
		e.StrategyType = models.StrategyType(strategyType)
		e.Action = models.TradeAction(action)
		e.Status = models.ExecutionStatus(status)
		_ = json.Unmarshal([]byte(metadata), &e.Metadata)
		out = append(out, e)
	}
	return out, rows.Err()
}

// SavePerformance persists a performance snapshot.
func (s *Store) SavePerformance(perf *models.StrategyPerformance) error {
	data, err := json.Marshal(perf)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO performance(strategy_id,performance_json,updated_at) VALUES (?,?,?)
		ON CONFLICT(strategy_id) DO UPDATE SET performance_json=excluded.performance_json,updated_at=excluded.updated_at`,
		perf.StrategyID, string(data), formatTime(perf.LastUpdated))
	return err
}

// GetPerformance loads a performance snapshot.
func (s *Store) GetPerformance(strategyID string) (*models.StrategyPerformance, error) {
	var data string
	if err := s.db.QueryRow(`SELECT performance_json FROM performance WHERE strategy_id=?`, strategyID).Scan(&data); err != nil {
		return nil, err
	}
	var perf models.StrategyPerformance
	if err := json.Unmarshal([]byte(data), &perf); err != nil {
		return nil, err
	}
	return &perf, nil
}

// SaveManagedOffer upserts a strategy-owned offer mapping.
func (s *Store) SaveManagedOffer(offer *models.ManagedOffer) error {
	baseJSON, _ := json.Marshal(offer.BaseAsset)
	quoteJSON, _ := json.Marshal(offer.QuoteAsset)
	_, err := s.db.Exec(`INSERT INTO managed_offers
		(strategy_id,intent_id,side,base_asset_json,quote_asset_json,price,amount,passive,status,offer_id,tx_hash,last_error,created_at,updated_at,submitted_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(strategy_id,intent_id) DO UPDATE SET side=excluded.side,base_asset_json=excluded.base_asset_json,
		quote_asset_json=excluded.quote_asset_json,price=excluded.price,amount=excluded.amount,passive=excluded.passive,
		status=excluded.status,offer_id=CASE WHEN excluded.offer_id != 0 THEN excluded.offer_id ELSE managed_offers.offer_id END,
		tx_hash=CASE WHEN excluded.tx_hash != '' THEN excluded.tx_hash ELSE managed_offers.tx_hash END,
		last_error=excluded.last_error,updated_at=excluded.updated_at,submitted_at=excluded.submitted_at`,
		offer.StrategyID, offer.IntentID, string(offer.Side), string(baseJSON), string(quoteJSON), offer.Price,
		offer.Amount, offer.Passive, offer.Status, offer.OfferID, offer.TxHash, offer.LastError,
		formatTime(offer.CreatedAt), formatTime(offer.UpdatedAt), timePtrString(offer.SubmittedAt))
	return err
}

// ListManagedOffers returns all managed offers for a strategy.
func (s *Store) ListManagedOffers(strategyID string) ([]*models.ManagedOffer, error) {
	rows, err := s.db.Query(`SELECT strategy_id,intent_id,side,base_asset_json,quote_asset_json,price,amount,passive,status,
		offer_id,tx_hash,last_error,created_at,updated_at,submitted_at FROM managed_offers WHERE strategy_id=? ORDER BY intent_id`, strategyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var offers []*models.ManagedOffer
	for rows.Next() {
		offer, err := scanManagedOffer(rows)
		if err != nil {
			return nil, err
		}
		offers = append(offers, offer)
	}
	return offers, rows.Err()
}

// MarkOffersCanceled marks records canceled.
func (s *Store) MarkOffersCanceled(strategyID string, offerIDs []int64) error {
	for _, id := range offerIDs {
		if _, err := s.db.Exec(`UPDATE managed_offers SET status='canceled',updated_at=? WHERE strategy_id=? AND offer_id=?`, formatTime(time.Now()), strategyID, id); err != nil {
			return err
		}
	}
	return nil
}

// SaveRuntime upserts strategy runtime state.
func (s *Store) SaveRuntime(runtime *models.StrategyRuntime) error {
	_, err := s.db.Exec(`INSERT INTO runtime(strategy_id,pid,started_at,last_heartbeat,last_error,consecutive_errors,cycles,dry_run,kill_switch)
		VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(strategy_id) DO UPDATE SET pid=excluded.pid,started_at=excluded.started_at,last_heartbeat=excluded.last_heartbeat,
		last_error=excluded.last_error,consecutive_errors=excluded.consecutive_errors,cycles=excluded.cycles,dry_run=excluded.dry_run,kill_switch=excluded.kill_switch`,
		runtime.StrategyID, runtime.PID, timePtrString(runtime.StartedAt), timePtrString(runtime.LastHeartbeat),
		runtime.LastError, runtime.ConsecutiveErr, runtime.Cycles, runtime.DryRun, runtime.KillSwitch)
	return err
}

// GetRuntime loads runtime state.
func (s *Store) GetRuntime(strategyID string) (*models.StrategyRuntime, error) {
	row := s.db.QueryRow(`SELECT strategy_id,pid,started_at,last_heartbeat,last_error,consecutive_errors,cycles,dry_run,kill_switch
		FROM runtime WHERE strategy_id=?`, strategyID)
	var runtime models.StrategyRuntime
	var started, heartbeat sql.NullString
	if err := row.Scan(&runtime.StrategyID, &runtime.PID, &started, &heartbeat, &runtime.LastError,
		&runtime.ConsecutiveErr, &runtime.Cycles, &runtime.DryRun, &runtime.KillSwitch); err != nil {
		return nil, err
	}
	if started.Valid {
		t := parseTime(started.String)
		runtime.StartedAt = &t
	}
	if heartbeat.Valid {
		t := parseTime(heartbeat.String)
		runtime.LastHeartbeat = &t
	}
	return &runtime, nil
}

// ClearRuntime deletes runtime state.
func (s *Store) ClearRuntime(strategyID string) error {
	_, err := s.db.Exec(`DELETE FROM runtime WHERE strategy_id=?`, strategyID)
	return err
}

// SaveFill records an observed fill.
func (s *Store) SaveFill(fill *models.Fill) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO fills
		(id,strategy_id,intent_id,offer_id,trade_id,price,amount,counter,tx_hash,executed_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		fill.ID, fill.StrategyID, fill.IntentID, fill.OfferID, fill.TradeID, fill.Price, fill.Amount,
		fill.Counter, fill.TxHash, formatTime(fill.ExecutedAt))
	return err
}

// HasFill reports whether a fill was already recorded.
func (s *Store) HasFill(id string) (bool, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(1) FROM fills WHERE id=?`, id).Scan(&count)
	return count > 0, err
}

// ListFills returns recent fills for a strategy.
func (s *Store) ListFills(strategyID string, limit int) ([]models.Fill, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT id,strategy_id,intent_id,offer_id,trade_id,price,amount,counter,tx_hash,executed_at
		FROM fills WHERE strategy_id=? ORDER BY executed_at DESC LIMIT ?`, strategyID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var fills []models.Fill
	for rows.Next() {
		var fill models.Fill
		var executed string
		if err := rows.Scan(&fill.ID, &fill.StrategyID, &fill.IntentID, &fill.OfferID, &fill.TradeID,
			&fill.Price, &fill.Amount, &fill.Counter, &fill.TxHash, &executed); err != nil {
			return nil, err
		}
		fill.ExecutedAt = parseTime(executed)
		fills = append(fills, fill)
	}
	return fills, rows.Err()
}

// SaveBacktest records a backtest result.
func (s *Store) SaveBacktest(result *models.BacktestResult) error {
	_, err := s.db.Exec(`INSERT INTO backtests
		(strategy_id,strategy_name,base_asset,quote_asset,network,iterations,desired_orders,buys,sells,trades,realized_pnl,ending_value,started_at,ended_at,created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		result.StrategyID, result.StrategyName, result.BaseAsset, result.QuoteAsset, string(result.Network),
		result.Iterations, result.DesiredOrders, result.Buys, result.Sells, result.Trades, result.RealizedPnL,
		result.EndingValue, formatTime(result.StartedAt), formatTime(result.EndedAt), formatTime(result.CreatedAt))
	return err
}

// SetKV stores a simple key/value.
func (s *Store) SetKV(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO kv_state(key,value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// GetKV retrieves a simple key/value.
func (s *Store) GetKV(key string) (string, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM kv_state WHERE key=?`, key).Scan(&value)
	return value, err
}

type scanner interface {
	Scan(dest ...interface{}) error
}

func scanStrategy(row scanner) (*models.TradingStrategy, error) {
	var strategy models.TradingStrategy
	var strategyType, status, network string
	var paramsJSON, riskJSON string
	var created, updated string
	var lastRun, active sql.NullString
	if err := row.Scan(&strategy.ID, &strategy.Name, &strategyType, &strategy.Description, &status, &network, &strategy.Wallet,
		&strategy.BaseAsset, &strategy.QuoteAsset, &paramsJSON, &riskJSON, &created, &updated, &lastRun,
		&strategy.TotalTrades, &strategy.TotalProfit, &active); err != nil {
		return nil, err
	}
	strategy.Type = models.StrategyType(strategyType)
	strategy.Status = models.StrategyStatus(status)
	strategy.Network = models.Network(network)
	if err := json.Unmarshal([]byte(paramsJSON), &strategy.Parameters); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(riskJSON), &strategy.RiskLimits); err != nil {
		return nil, err
	}
	strategy.CreatedAt = parseTime(created)
	strategy.UpdatedAt = parseTime(updated)
	if lastRun.Valid {
		t := parseTime(lastRun.String)
		strategy.LastRunAt = &t
	}
	if active.Valid {
		t := parseTime(active.String)
		strategy.ActiveSince = &t
	}
	return &strategy, nil
}

func scanManagedOffer(row scanner) (*models.ManagedOffer, error) {
	var offer models.ManagedOffer
	var side, status string
	var baseJSON, quoteJSON string
	var created, updated string
	var submitted sql.NullString
	if err := row.Scan(&offer.StrategyID, &offer.IntentID, &side, &baseJSON, &quoteJSON, &offer.Price,
		&offer.Amount, &offer.Passive, &status, &offer.OfferID, &offer.TxHash, &offer.LastError,
		&created, &updated, &submitted); err != nil {
		return nil, err
	}
	offer.Side = models.OrderSide(side)
	offer.Status = status
	_ = json.Unmarshal([]byte(baseJSON), &offer.BaseAsset)
	_ = json.Unmarshal([]byte(quoteJSON), &offer.QuoteAsset)
	offer.CreatedAt = parseTime(created)
	offer.UpdatedAt = parseTime(updated)
	if submitted.Valid {
		t := parseTime(submitted.String)
		offer.SubmittedAt = &t
	}
	return &offer, nil
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func timePtrString(t *time.Time) interface{} {
	if t == nil {
		return nil
	}
	return formatTime(*t)
}

func parseTime(value string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return t
}
