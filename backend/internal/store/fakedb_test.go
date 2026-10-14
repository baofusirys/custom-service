package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

// fakedb_test.go [115] 测试用最小 database/sql 驱动：记录每条 SQL 与参数，按处理函数返回结果。
// 用于断言「每类一条批量 SQL、无逐行查询」、参数化占位符个数与参数一致、权限 SQL 分支等，不依赖真实 MySQL。

type fakeResult struct {
	cols []string
	rows [][]driver.Value
	err  error
}

type fakeCall struct {
	query string
	args  []driver.Value
}

type fakeDB struct {
	mu             sync.Mutex
	handler        func(query string, args []driver.Value) fakeResult
	calls          []fakeCall
	begins         int
	readOnlyBegins int
	commits        int
	rollbacks      int
}

func newFakeStore(t *testing.T, h func(query string, args []driver.Value) fakeResult) (*Store, *fakeDB) {
	t.Helper()
	f := &fakeDB{handler: h}
	db := sql.OpenDB(f)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return New(db), f
}

func (f *fakeDB) snapshot() []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeCall(nil), f.calls...)
}

func (f *fakeDB) Connect(context.Context) (driver.Conn, error) { return &fakeConn{f: f}, nil }
func (f *fakeDB) Driver() driver.Driver                        { return fakeDriver{f: f} }

type fakeDriver struct{ f *fakeDB }

func (d fakeDriver) Open(string) (driver.Conn, error) { return &fakeConn{f: d.f}, nil }

type fakeConn struct{ f *fakeDB }

func (c *fakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("fake: prepare not supported")
}
func (c *fakeConn) Close() error { return nil }
func (c *fakeConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *fakeConn) BeginTx(_ context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	c.f.begins++
	if opts.ReadOnly {
		c.f.readOnlyBegins++
	}
	return &fakeTx{f: c.f}, nil
}
func (c *fakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	vals := make([]driver.Value, len(args))
	for i, a := range args {
		vals[i] = a.Value
	}
	c.f.mu.Lock()
	c.f.calls = append(c.f.calls, fakeCall{query: query, args: vals})
	h := c.f.handler
	c.f.mu.Unlock()
	res := h(query, vals)
	if res.err != nil {
		return nil, res.err
	}
	return &fakeRows{cols: res.cols, rows: res.rows}, nil
}

type fakeTx struct{ f *fakeDB }

func (t *fakeTx) Commit() error {
	t.f.mu.Lock()
	defer t.f.mu.Unlock()
	t.f.commits++
	return nil
}
func (t *fakeTx) Rollback() error {
	t.f.mu.Lock()
	defer t.f.mu.Unlock()
	t.f.rollbacks++
	return nil
}

type fakeRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *fakeRows) Columns() []string { return r.cols }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

// cols 生成 n 个占位列名（扫描只按位置，列名无关）。
func cols(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "c"
	}
	return out
}

// assertPlaceholders 参数化校验：SQL 中 ? 个数必须等于实际参数个数（防拼接值进 SQL）。
func assertPlaceholders(t *testing.T, c fakeCall) {
	t.Helper()
	if got := strings.Count(c.query, "?"); got != len(c.args) {
		t.Fatalf("占位符 %d 个与参数 %d 个不一致: %s", got, len(c.args), c.query)
	}
}
