package xorm

import (
	"bufio"
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"

	"github.com/go-xorm/core"
)

// Engine is the major struct of xorm, it means a database manager.
// Commonly, an application only need one engine
type Engine struct {
	ColumnMapper   core.IMapper
	TableMapper    core.IMapper
	TagIdentifier  string
	DriverName     string
	DataSourceName string
	dialect        core.Dialect
	Tables         map[reflect.Type]*core.Table

	mutex     *sync.RWMutex
	ShowSQL   bool
	ShowErr   bool
	ShowDebug bool
	ShowWarn  bool
	Pool      IConnectPool
	Filters   []core.Filter
	Logger    ILogger // io.Writer
	Cacher    core.Cacher
}

func (engine *Engine) SetMapper(mapper core.IMapper) {
	engine.SetTableMapper(mapper)
	engine.SetColumnMapper(mapper)
}

func (engine *Engine) SetTableMapper(mapper core.IMapper) {
	engine.TableMapper = mapper
}

func (engine *Engine) SetColumnMapper(mapper core.IMapper) {
	engine.ColumnMapper = mapper
}

// If engine's database support batch insert records like
// "insert into user values (name, age), (name, age)".
// When the return is ture, then engine.Insert(&users) will
// generate batch sql and exeute.
func (engine *Engine) SupportInsertMany() bool {
	return engine.dialect.SupportInsertMany()
}

// Engine's database use which charactor as quote.
// mysql, sqlite use ` and postgres use "
func (engine *Engine) QuoteStr() string {
	return engine.dialect.QuoteStr()
}

// Use QuoteStr quote the string sql
func (engine *Engine) Quote(sql string) string {
	return engine.dialect.QuoteStr() + sql + engine.dialect.QuoteStr()
}

// A simple wrapper to dialect's core.SqlType method
func (engine *Engine) SqlType(c *core.Column) string {
	return engine.dialect.SqlType(c)
}

// Database's autoincrement statement
func (engine *Engine) AutoIncrStr() string {
	return engine.dialect.AutoIncrStr()
}

// Set engine's pool, the pool default is Go's standard library's connection pool.
func (engine *Engine) SetPool(pool IConnectPool) error {
	engine.Pool = pool
	return engine.Pool.Init(engine)
}

// SetMaxConns is only available for go 1.2+
func (engine *Engine) SetMaxConns(conns int) {
	engine.Pool.SetMaxConns(conns)
}

// SetMaxIdleConns
func (engine *Engine) SetMaxIdleConns(conns int) {
	engine.Pool.SetMaxIdleConns(conns)
}

// SetDefaltCacher set the default cacher. Xorm's default not enable cacher.
func (engine *Engine) SetDefaultCacher(cacher core.Cacher) {
	engine.Cacher = cacher
}

// If you has set default cacher, and you want temporilly stop use cache,
// you can use NoCache()
func (engine *Engine) NoCache() *Session {
	session := engine.NewSession()
	session.IsAutoClose = true
	return session.NoCache()
}

func (engine *Engine) NoCascade() *Session {
	session := engine.NewSession()
	session.IsAutoClose = true
	return session.NoCascade()
}

// Set a table use a special cacher
func (engine *Engine) MapCacher(bean interface{}, cacher core.Cacher) {
	v := rValue(bean)
	engine.autoMapType(v)
	engine.Tables[v.Type()].Cacher = cacher
}

// OpenDB provides a interface to operate database directly.
func (engine *Engine) OpenDB() (*core.DB, error) {
	return core.Open(engine.DriverName, engine.DataSourceName)
}

// New a session
func (engine *Engine) NewSession() *Session {
	session := &Session{Engine: engine}
	session.Init()
	return session
}

// Close the engine
func (engine *Engine) Close() error {
	return engine.Pool.Close(engine)
}

// Ping tests if database is alive.
func (engine *Engine) Ping() error {
	session := engine.NewSession()
	defer session.Close()
	engine.LogInfo("PING DATABASE", engine.DriverName)
	return session.Ping()
}

// logging sql
func (engine *Engine) logSQL(sqlStr string, sqlArgs ...interface{}) {
	if engine.ShowSQL {
		if len(sqlArgs) > 0 {
			engine.LogInfo("[sql]", sqlStr, "[args]", sqlArgs)
		} else {
			engine.LogInfo("[sql]", sqlStr)
		}
	}
}

// logging error
func (engine *Engine) LogError(contents ...interface{}) {
	if engine.ShowErr {
		engine.Logger.Err(fmt.Sprintln(contents...))
	}
}

// logging error
func (engine *Engine) LogInfo(contents ...interface{}) {
	engine.Logger.Info(fmt.Sprintln(contents...))
}

// logging debug
func (engine *Engine) LogDebug(contents ...interface{}) {
	if engine.ShowDebug {
		engine.Logger.Debug(fmt.Sprintln(contents...))
	}
}

// logging warn
func (engine *Engine) LogWarn(contents ...interface{}) {
	if engine.ShowWarn {
		engine.Logger.Warning(fmt.Sprintln(contents...))
	}
}

// Sql method let's you manualy write raw sql and operate
// For example:
//
//         engine.Sql("select * from user").Find(&users)
//
// This    code will execute "select * from user" and set the records to users
//
func (engine *Engine) Sql(querystring string, args ...interface{}) *Session {
	session := engine.NewSession()
	session.IsAutoClose = true
	return session.Sql(querystring, args...)
}

// Default if your struct has "created" or "updated" filed tag, the fields
// will automatically be filled with current time when Insert or Update
// invoked. Call NoAutoTime if you dont' want to fill automatically.
func (engine *Engine) NoAutoTime() *Session {
	session := engine.NewSession()
	session.IsAutoClose = true
	return session.NoAutoTime()
}

// Retrieve all tables, columns, indexes' informations from database.
func (engine *Engine) DBMetas() ([]*core.Table, error) {
	tables, err := engine.dialect.GetTables()
	if err != nil {
		return nil, err
	}

	for _, table := range tables {
		colSeq, cols, err := engine.dialect.GetColumns(table.Name)
		if err != nil {
			return nil, err
		}
		for _, name := range colSeq {
			table.AddColumn(cols[name])
		}
		//table.Columns = cols
		//table.ColumnsSeq = colSeq

		indexes, err := engine.dialect.GetIndexes(table.Name)
		if err != nil {
			return nil, err
		}
		table.Indexes = indexes

		for _, index := range indexes {
			for _, name := range index.Cols {
				if col := table.GetColumn(name); col != nil {
					col.Indexes[index.Name] = true
				} else {
					return nil, fmt.Errorf("Unknown col "+name+" in indexes %v", index)
				}
			}
		}
	}
	return tables, nil
}

// use cascade or not
func (engine *Engine) Cascade(trueOrFalse ...bool) *Session {
	session := engine.NewSession()
	session.IsAutoClose = true
	return session.Cascade(trueOrFalse...)
}

// Where method provide a condition query
func (engine *Engine) Where(querystring string, args ...interface{}) *Session {
	session := engine.NewSession()
	session.IsAutoClose = true
	return session.Where(querystring, args...)
}

// Id mehtod provoide a condition as (id) = ?
func (engine *Engine) Id(id interface{}) *Session {
	session := engine.NewSession()
	session.IsAutoClose = true
	return session.Id(id)
}

// Apply before Processor, affected bean is passed to closure arg
func (engine *Engine) Before(closures func(interface{})) *Session {
	session := engine.NewSession()
	session.IsAutoClose = true
	return session.Before(closures)
}

// Apply after insert Processor, affected bean is passed to closure arg
func (engine *Engine) After(closures func(interface{})) *Session {
	session := engine.NewSession()
	session.IsAutoClose = true
	return session.After(closures)
}

// set charset when create table, only support mysql now
func (engine *Engine) Charset(charset string) *Session {
	session := engine.NewSession()
	session.IsAutoClose = true
	return session.Charset(charset)
}

// set store engine when create table, only support mysql now
func (engine *Engine) StoreEngine(storeEngine string) *Session {
	session := engine.NewSession()
	session.IsAutoClose = true
	return session.StoreEngine(storeEngine)
}

// use for distinct columns. Caution: when you are using cache,
// distinct will not be cached because cache system need id,
// but distinct will not provide id
func (engine *Engine) Distinct(columns ...string) *Session {
	session := engine.NewSession()
	session.IsAutoClose = true
	return session.Distinct(columns...)
}

// only use the paramters as select or update columns
func (engine *Engine) Cols(columns ...string) *Session {
	session := engine.NewSession()
	session.IsAutoClose = true
	return session.Cols(columns...)
}

func (engine *Engine) AllCols() *Session {
	session := engine.NewSession()
	session.IsAutoClose = true
	return session.AllCols()
}

func (engine *Engine) MustCols(columns ...string) *Session {
	session := engine.NewSession()
	session.IsAutoClose = true
	return session.MustCols(columns...)
}

// Xorm automatically retrieve condition according struct, but
// if struct has bool field, it will ignore them. So use UseBool
// to tell system to do not ignore them.
// If no paramters, it will use all the bool field of struct, or
// it will use paramters's columns
func (engine *Engine) UseBool(columns ...string) *Session {
	session := engine.NewSession()
	session.IsAutoClose = true
	return session.UseBool(columns...)
}

// Only not use the paramters as select or update columns
func (engine *Engine) Omit(columns ...string) *Session {
	session := engine.NewSession()
	session.IsAutoClose = true
	return session.Omit(columns...)
}

// This method will generate "column IN (?, ?)"
func (engine *Engine) In(column string, args ...interface{}) *Session {
	session := engine.NewSession()
	session.IsAutoClose = true
	return session.In(column, args...)
}

// Temporarily change the Get, Find, Update's table
func (engine *Engine) Table(tableNameOrBean interface{}) *Session {
	session := engine.NewSession()
	session.IsAutoClose = true
	return session.Table(tableNameOrBean)
}

// This method will generate "LIMIT start, limit"
func (engine *Engine) Limit(limit int, start ...int) *Session {
	session := engine.NewSession()
	session.IsAutoClose = true
	return session.Limit(limit, start...)
}

// Method Desc will generate "ORDER BY column1 DESC, column2 DESC"
// This will
func (engine *Engine) Desc(colNames ...string) *Session {
	session := engine.NewSession()
	session.IsAutoClose = true
	return session.Desc(colNames...)
}

// Method Asc will generate "ORDER BY column1 DESC, column2 Asc"
// This method can chainable use.
//
//        engine.Desc("name").Asc("age").Find(&users)
//        // SELECT * FROM user ORDER BY name DESC, age ASC
//
func (engine *Engine) Asc(colNames ...string) *Session {
	session := engine.NewSession()
	session.IsAutoClose = true
	return session.Asc(colNames...)
}

// Method OrderBy will generate "ORDER BY order"
func (engine *Engine) OrderBy(order string) *Session {
	session := engine.NewSession()
	session.IsAutoClose = true
	return session.OrderBy(order)
}

// The join_operator should be one of INNER, LEFT OUTER, CROSS etc - this will be prepended to JOIN
func (engine *Engine) Join(join_operator, tablename, condition string) *Session {
	session := engine.NewSession()
	session.IsAutoClose = true
	return session.Join(join_operator, tablename, condition)
}

// Generate Group By statement
func (engine *Engine) GroupBy(keys string) *Session {
	session := engine.NewSession()
	session.IsAutoClose = true
	return session.GroupBy(keys)
}

// Generate Having statement
func (engine *Engine) Having(conditions string) *Session {
	session := engine.NewSession()
	session.IsAutoClose = true
	return session.Having(conditions)
}

func (engine *Engine) autoMapType(v reflect.Value) *core.Table {
	t := v.Type()
	engine.mutex.RLock()
	table, ok := engine.Tables[t]
	engine.mutex.RUnlock()
	if !ok {
		table = engine.mapType(v)
		engine.mutex.Lock()
		engine.Tables[t] = table
		engine.mutex.Unlock()
	}
	return table
}

func (engine *Engine) autoMap(bean interface{}) *core.Table {
	v := rValue(bean)
	return engine.autoMapType(v)
}

/*func (engine *Engine) mapType(t reflect.Type) *core.Table {
	return mappingTable(t, engine.TableMapper, engine.ColumnMapper, engine.dialect, engine.TagIdentifier)
}*/

/*
func mappingTable(t reflect.Type, tableMapper core.IMapper, colMapper core.IMapper, dialect core.Dialect, tagId string) *core.Table {
	table := core.NewEmptyTable()
	table.Name = tableMapper.Obj2Table(t.Name())
*/
func addIndex(indexName string, table *core.Table, col *core.Column, indexType int) {
	if index, ok := table.Indexes[indexName]; ok {
		index.AddColumn(col.Name)
		col.Indexes[index.Name] = true
	} else {
		index := core.NewIndex(indexName, indexType)
		index.AddColumn(col.Name)
		table.AddIndex(index)
		col.Indexes[index.Name] = true
	}
}

func (engine *Engine) newTable() *core.Table {
	table := core.NewEmptyTable()
	table.Cacher = engine.Cacher
	return table
}

func (engine *Engine) mapType(v reflect.Value) *core.Table {
	t := v.Type()
	table := engine.newTable()
	method := v.MethodByName("TableName")
	if !method.IsValid() {
		method = v.Addr().MethodByName("TableName")
	}
	if method.IsValid() {
		params := []reflect.Value{}
		results := method.Call(params)
		if len(results) == 1 {
			table.Name = results[0].Interface().(string)
		}
	}

	if table.Name == "" {
		table.Name = engine.TableMapper.Obj2Table(t.Name())
	}
	table.Type = t

	var idFieldColName string
	var err error

	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag
		ormTagStr := tag.Get(engine.TagIdentifier)
		var col *core.Column
		fieldValue := v.Field(i)
		fieldType := fieldValue.Type()

		if ormTagStr != "" {
			col = &core.Column{FieldName: t.Field(i).Name, Nullable: true, IsPrimaryKey: false,
				IsAutoIncrement: false, MapType: core.TWOSIDES, Indexes: make(map[string]bool)}
			tags := strings.Split(ormTagStr, " ")

			if len(tags) > 0 {
				if tags[0] == "-" {
					continue
				}
				if (strings.ToUpper(tags[0]) == "EXTENDS") &&
					(fieldType.Kind() == reflect.Struct) {

					//parentTable := mappingTable(fieldType, tableMapper, colMapper, dialect, tagId)
					parentTable := engine.mapType(fieldValue)
					for _, col := range parentTable.Columns() {
						col.FieldName = fmt.Sprintf("%v.%v", fieldType.Name(), col.FieldName)
						table.AddColumn(col)
					}

					continue
				}

				indexNames := make(map[string]int)
				var isIndex, isUnique bool
				var preKey string
				for j, key := range tags {
					k := strings.ToUpper(key)
					switch {
					case k == "<-":
						col.MapType = core.ONLYFROMDB
					case k == "->":
						col.MapType = core.ONLYTODB
					case k == "PK":
						col.IsPrimaryKey = true
						col.Nullable = false
					case k == "NULL":
						col.Nullable = (strings.ToUpper(tags[j-1]) != "NOT")
					/*case strings.HasPrefix(k, "AUTOINCR(") && strings.HasSuffix(k, ")"):
					col.IsAutoIncrement = true

					autoStart := k[len("AUTOINCR")+1 : len(k)-1]
					autoStartInt, err := strconv.Atoi(autoStart)
					if err != nil {
						engine.LogError(err)
					}
					col.AutoIncrStart = autoStartInt*/
					case k == "AUTOINCR":
						col.IsAutoIncrement = true
						//col.AutoIncrStart = 1
					case k == "DEFAULT":
						col.Default = tags[j+1]
					case k == "CREATED":
						col.IsCreated = true
					case k == "VERSION":
						col.IsVersion = true
						col.Default = "1"
					case k == "UPDATED":
						col.IsUpdated = true
					case strings.HasPrefix(k, "INDEX(") && strings.HasSuffix(k, ")"):
						indexName := k[len("INDEX")+1 : len(k)-1]
						indexNames[indexName] = core.IndexType
					case k == "INDEX":
						isIndex = true
					case strings.HasPrefix(k, "UNIQUE(") && strings.HasSuffix(k, ")"):
						indexName := k[len("UNIQUE")+1 : len(k)-1]
						indexNames[indexName] = core.UniqueType
					case k == "UNIQUE":
						isUnique = true
					case k == "NOTNULL":
						col.Nullable = false
					case k == "NOT":
					default:
						if strings.HasPrefix(k, "'") && strings.HasSuffix(k, "'") {
							if preKey != "DEFAULT" {
								col.Name = key[1 : len(key)-1]
							}
						} else if strings.Contains(k, "(") && strings.HasSuffix(k, ")") {
							fs := strings.Split(k, "(")

							if _, ok := core.SqlTypes[fs[0]]; !ok {
								preKey = k
								continue
							}
							col.SQLType = core.SQLType{fs[0], 0, 0}
							fs2 := strings.Split(fs[1][0:len(fs[1])-1], ",")
							if len(fs2) == 2 {
								col.Length, err = strconv.Atoi(fs2[0])
								if err != nil {
									engine.LogError(err)
								}
								col.Length2, err = strconv.Atoi(fs2[1])
								if err != nil {
									engine.LogError(err)
								}
							} else if len(fs2) == 1 {
								col.Length, err = strconv.Atoi(fs2[0])
								if err != nil {
									engine.LogError(err)
								}
							}
						} else {
							if _, ok := core.SqlTypes[k]; ok {
								col.SQLType = core.SQLType{k, 0, 0}
							} else if key != col.Default {
								col.Name = key
							}
						}
						panic("broken")
						//engine.dialect.SqlType(col)
					}
					preKey = k
				}
				if col.SQLType.Name == "" {
					col.SQLType = core.Type2SQLType(fieldType)
				}
				if col.Length == 0 {
					col.Length = col.SQLType.DefaultLength
				}
				if col.Length2 == 0 {
					col.Length2 = col.SQLType.DefaultLength2
				}
				//fmt.Println("======", col)
				if col.Name == "" {
					col.Name = engine.ColumnMapper.Obj2Table(t.Field(i).Name)
				}

				if isUnique {
					indexNames[col.Name] = core.UniqueType
				} else if isIndex {
					indexNames[col.Name] = core.IndexType
				}

				for indexName, indexType := range indexNames {
					addIndex(indexName, table, col, indexType)
				}
			}
		} else {
			sqlType := core.Type2SQLType(fieldType)
			col = core.NewColumn(engine.ColumnMapper.Obj2Table(t.Field(i).Name),
				t.Field(i).Name, sqlType, sqlType.DefaultLength,
				sqlType.DefaultLength2, true)
		}
		if col.IsAutoIncrement {
			col.Nullable = false
		}

		table.AddColumn(col)

		if fieldType.Kind() == reflect.Int64 && (col.FieldName == "Id" || strings.HasSuffix(col.FieldName, ".Id")) {
			idFieldColName = col.Name
		}
	}

	if idFieldColName != "" && len(table.PrimaryKeys) == 0 {
		col := table.GetColumn(idFieldColName)
		col.IsPrimaryKey = true
		col.IsAutoIncrement = true
		col.Nullable = false
		table.PrimaryKeys = append(table.PrimaryKeys, col.Name)
		table.AutoIncrement = col.Name
	}

	return table
}

// Map a struct to a table
func (engine *Engine) mapping(beans ...interface{}) (e error) {
	engine.mutex.Lock()
	defer engine.mutex.Unlock()
	for _, bean := range beans {
		v := rValue(bean)
		engine.Tables[v.Type()] = engine.mapType(v)
	}
	return
}

// If a table has any reocrd
func (engine *Engine) IsTableEmpty(bean interface{}) (bool, error) {
	v := rValue(bean)
	t := v.Type()
	if t.Kind() != reflect.Struct {
		return false, errors.New("bean should be a struct or struct's point")
	}
	engine.autoMapType(v)
	session := engine.NewSession()
	defer session.Close()
	rows, err := session.Count(bean)
	return rows > 0, err
}

// If a table is exist
func (engine *Engine) IsTableExist(bean interface{}) (bool, error) {
	v := rValue(bean)
	if v.Type().Kind() != reflect.Struct {
		return false, errors.New("bean should be a struct or struct's point")
	}
	table := engine.autoMapType(v)
	session := engine.NewSession()
	defer session.Close()
	has, err := session.isTableExist(table.Name)
	return has, err
}

func (engine *Engine) IdOf(bean interface{}) core.PK {
	table := engine.autoMap(bean)
	v := reflect.Indirect(reflect.ValueOf(bean))
	pk := make([]interface{}, len(table.PrimaryKeys))
	for i, col := range table.PKColumns() {
		pkField := v.FieldByName(col.FieldName)
		switch pkField.Kind() {
		case reflect.String:
			pk[i] = pkField.String()
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			pk[i] = pkField.Int()
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			pk[i] = pkField.Uint()
		}
	}
	return core.PK(pk)
}

// create indexes
func (engine *Engine) CreateIndexes(bean interface{}) error {
	session := engine.NewSession()
	defer session.Close()
	return session.CreateIndexes(bean)
}

// create uniques
func (engine *Engine) CreateUniques(bean interface{}) error {
	session := engine.NewSession()
	defer session.Close()
	return session.CreateUniques(bean)
}

func (engine *Engine) getCacher2(table *core.Table) core.Cacher {
	return table.Cacher
}

func (engine *Engine) getCacher(v reflect.Value) core.Cacher {
	if table := engine.autoMapType(v); table != nil {
		return table.Cacher
	}
	return engine.Cacher
}

// If enabled cache, clear the cache bean
func (engine *Engine) ClearCacheBean(bean interface{}, id string) error {
	t := rType(bean)
	if t.Kind() != reflect.Struct {
		return errors.New("error params")
	}
	table := engine.autoMap(bean)
	cacher := table.Cacher
	if cacher == nil {
		cacher = engine.Cacher
	}
	if cacher != nil {
		cacher.ClearIds(table.Name)
		cacher.DelBean(table.Name, id)
	}
	return nil
}

// If enabled cache, clear some tables' cache
func (engine *Engine) ClearCache(beans ...interface{}) error {
	for _, bean := range beans {
		t := rType(bean)
		if t.Kind() != reflect.Struct {
			return errors.New("error params")
		}
		table := engine.autoMap(bean)
		cacher := table.Cacher
		if cacher == nil {
			cacher = engine.Cacher
		}
		if cacher != nil {
			cacher.ClearIds(table.Name)
			cacher.ClearBeans(table.Name)
		}
	}
	return nil
}

// Sync the new struct changes to database, this method will automatically add
// table, column, index, unique. but will not delete or change anything.
// If you change some field, you should change the database manually.
func (engine *Engine) Sync(beans ...interface{}) error {
	for _, bean := range beans {
		table := engine.autoMap(bean)

		s := engine.NewSession()
		defer s.Close()
		isExist, err := s.Table(bean).isTableExist(table.Name)
		if err != nil {
			return err
		}
		if !isExist {
			err = engine.CreateTables(bean)
			if err != nil {
				return err
			}
		}
		/*isEmpty, err := engine.IsEmptyTable(bean)
		  if err != nil {
		      return err
		  }*/
		var isEmpty bool = false
		if isEmpty {
			err = engine.DropTables(bean)
			if err != nil {
				return err
			}
			err = engine.CreateTables(bean)
			if err != nil {
				return err
			}
		} else {
			for _, col := range table.Columns() {
				session := engine.NewSession()
				session.Statement.RefTable = table
				defer session.Close()
				isExist, err := session.isColumnExist(table.Name, col.Name)
				if err != nil {
					return err
				}
				if !isExist {
					session := engine.NewSession()
					session.Statement.RefTable = table
					defer session.Close()
					err = session.addColumn(col.Name)
					if err != nil {
						return err
					}
				}
			}

			for name, index := range table.Indexes {
				session := engine.NewSession()
				session.Statement.RefTable = table
				defer session.Close()
				if index.Type == core.UniqueType {
					//isExist, err := session.isIndexExist(table.Name, name, true)
					isExist, err := session.isIndexExist2(table.Name, index.Cols, true)
					if err != nil {
						return err
					}
					if !isExist {
						session := engine.NewSession()
						session.Statement.RefTable = table
						defer session.Close()
						err = session.addUnique(table.Name, name)
						if err != nil {
							return err
						}
					}
				} else if index.Type == core.IndexType {
					isExist, err := session.isIndexExist2(table.Name, index.Cols, false)
					if err != nil {
						return err
					}
					if !isExist {
						session := engine.NewSession()
						session.Statement.RefTable = table
						defer session.Close()
						err = session.addIndex(table.Name, name)
						if err != nil {
							return err
						}
					}
				} else {
					return errors.New("unknow index type")
				}
			}
		}
	}
	return nil
}

func (engine *Engine) unMap(beans ...interface{}) (e error) {
	engine.mutex.Lock()
	defer engine.mutex.Unlock()
	for _, bean := range beans {
		t := rType(bean)
		if _, ok := engine.Tables[t]; ok {
			delete(engine.Tables, t)
		}
	}
	return
}

// Drop all mapped table
func (engine *Engine) dropAll() error {
	session := engine.NewSession()
	defer session.Close()

	err := session.Begin()
	if err != nil {
		return err
	}
	err = session.dropAll()
	if err != nil {
		session.Rollback()
		return err
	}
	return session.Commit()
}

// CreateTables create tabls according bean
func (engine *Engine) CreateTables(beans ...interface{}) error {
	session := engine.NewSession()
	err := session.Begin()
	defer session.Close()
	if err != nil {
		return err
	}

	for _, bean := range beans {
		err = session.CreateTable(bean)
		if err != nil {
			session.Rollback()
			return err
		}
	}
	return session.Commit()
}

func (engine *Engine) DropTables(beans ...interface{}) error {
	session := engine.NewSession()
	err := session.Begin()
	defer session.Close()
	if err != nil {
		return err
	}

	for _, bean := range beans {
		err = session.DropTable(bean)
		if err != nil {
			session.Rollback()
			return err
		}
	}
	return session.Commit()
}

func (engine *Engine) createAll() error {
	session := engine.NewSession()
	defer session.Close()
	return session.createAll()
}

// Exec raw sql
func (engine *Engine) Exec(sql string, args ...interface{}) (sql.Result, error) {
	session := engine.NewSession()
	defer session.Close()
	return session.Exec(sql, args...)
}

// Exec a raw sql and return records as []map[string][]byte
func (engine *Engine) Query(sql string, paramStr ...interface{}) (resultsSlice []map[string][]byte, err error) {
	session := engine.NewSession()
	defer session.Close()
	return session.Query(sql, paramStr...)
}

// Insert one or more records
func (engine *Engine) Insert(beans ...interface{}) (int64, error) {
	session := engine.NewSession()
	defer session.Close()
	return session.Insert(beans...)
}

// Insert only one record
func (engine *Engine) InsertOne(bean interface{}) (int64, error) {
	session := engine.NewSession()
	defer session.Close()
	return session.InsertOne(bean)
}

// Update records, bean's non-empty fields are updated contents,
// condiBean' non-empty filds are conditions
// CAUTION:
//        1.bool will defaultly be updated content nor conditions
//         You should call UseBool if you have bool to use.
//        2.float32 & float64 may be not inexact as conditions
func (engine *Engine) Update(bean interface{}, condiBeans ...interface{}) (int64, error) {
	session := engine.NewSession()
	defer session.Close()
	return session.Update(bean, condiBeans...)
}

// Delete records, bean's non-empty fields are conditions
func (engine *Engine) Delete(bean interface{}) (int64, error) {
	session := engine.NewSession()
	defer session.Close()
	return session.Delete(bean)
}

// Get retrieve one record from table, bean's non-empty fields
// are conditions
func (engine *Engine) Get(bean interface{}) (bool, error) {
	session := engine.NewSession()
	defer session.Close()
	return session.Get(bean)
}

// Find retrieve records from table, condiBeans's non-empty fields
// are conditions. beans could be []Struct, []*Struct, map[int64]Struct
// map[int64]*Struct
func (engine *Engine) Find(beans interface{}, condiBeans ...interface{}) error {
	session := engine.NewSession()
	defer session.Close()
	return session.Find(beans, condiBeans...)
}

// Iterate record by record handle records from table, bean's non-empty fields
// are conditions.
func (engine *Engine) Iterate(bean interface{}, fun IterFunc) error {
	session := engine.NewSession()
	defer session.Close()
	return session.Iterate(bean, fun)
}

// Return sql.Rows compatible Rows obj, as a forward Iterator object for iterating record by record, bean's non-empty fields
// are conditions.
func (engine *Engine) Rows(bean interface{}) (*Rows, error) {
	session := engine.NewSession()
	return session.Rows(bean)
}

// Count counts the records. bean's non-empty fields
// are conditions.
func (engine *Engine) Count(bean interface{}) (int64, error) {
	session := engine.NewSession()
	defer session.Close()
	return session.Count(bean)
}

// Import SQL DDL file
func (engine *Engine) Import(ddlPath string) ([]sql.Result, error) {

	file, err := os.Open(ddlPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var results []sql.Result
	var lastError error
	scanner := bufio.NewScanner(file)

	semiColSpliter := func(data []byte, atEOF bool) (advance int, token []byte, err error) {
		if atEOF && len(data) == 0 {
			return 0, nil, nil
		}
		if i := bytes.IndexByte(data, ';'); i >= 0 {
			return i + 1, data[0:i], nil
		}
		// If we're at EOF, we have a final, non-terminated line. Return it.
		if atEOF {
			return len(data), data, nil
		}
		// Request more data.
		return 0, nil, nil
	}

	scanner.Split(semiColSpliter)

	session := engine.NewSession()
	defer session.Close()
	err = session.newDb()
	if err != nil {
		return results, err
	}

	for scanner.Scan() {
		query := scanner.Text()
		query = strings.Trim(query, " \t")
		if len(query) > 0 {
			result, err := session.Db.Exec(query)
			results = append(results, result)
			if err != nil {
				lastError = err
			}
		}
	}
	return results, lastError
}
