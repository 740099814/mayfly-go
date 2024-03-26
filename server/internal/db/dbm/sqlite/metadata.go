package sqlite

import (
	"errors"
	"fmt"
	"io"
	"mayfly-go/internal/db/dbm/dbi"
	"mayfly-go/pkg/logx"
	"mayfly-go/pkg/utils/anyx"
	"mayfly-go/pkg/utils/collx"
	"mayfly-go/pkg/utils/stringx"
	"regexp"
	"strings"
	"time"

	"github.com/may-fly/cast"
)

const (
	SQLITE_META_FILE      = "metasql/sqlite_meta.sql"
	SQLITE_TABLE_INFO_KEY = "SQLITE_TABLE_INFO"
	SQLITE_INDEX_INFO_KEY = "SQLITE_INDEX_INFO"
)

type SqliteMetaData struct {
	dbi.DefaultMetaData

	dc *dbi.DbConn
}

func (sd *SqliteMetaData) GetDbServer() (*dbi.DbServer, error) {
	_, res, err := sd.dc.Query("SELECT SQLITE_VERSION() as version")
	if err != nil {
		return nil, err
	}
	ds := &dbi.DbServer{
		Version: cast.ToString(res[0]["version"]),
	}
	return ds, nil
}

func (sd *SqliteMetaData) GetDbNames() ([]string, error) {
	databases := make([]string, 0)
	_, res, err := sd.dc.Query("PRAGMA database_list")
	if err != nil {
		return nil, err
	}
	for _, re := range res {
		databases = append(databases, cast.ToString(re["name"]))
	}

	return databases, nil
}

// 获取表基础元信息, 如表名等
func (sd *SqliteMetaData) GetTables(tableNames ...string) ([]dbi.Table, error) {
	names := strings.Join(collx.ArrayMap[string, string](tableNames, func(val string) string {
		return fmt.Sprintf("'%s'", dbi.RemoveQuote(sd, val))
	}), ",")

	var res []map[string]any
	var err error

	sql, err := stringx.TemplateParse(dbi.GetLocalSql(SQLITE_META_FILE, SQLITE_TABLE_INFO_KEY), collx.M{"tableNames": names})
	if err != nil {
		return nil, err
	}

	_, res, err = sd.dc.Query(sql)
	if err != nil {
		return nil, err
	}

	tables := make([]dbi.Table, 0)
	for _, re := range res {
		tables = append(tables, dbi.Table{
			TableName:    cast.ToString(re["tableName"]),
			TableComment: cast.ToString(re["tableComment"]),
			CreateTime:   cast.ToString(re["createTime"]),
			TableRows:    cast.ToInt(re["tableRows"]),
			DataLength:   cast.ToInt64(re["dataLength"]),
			IndexLength:  cast.ToInt64(re["indexLength"]),
		})
	}
	return tables, nil
}

// GetDataTypes 正则提取字段类型中的关键字，
// 如 decimal(10,2)  提取decimal, 10 ,2
// 如:text 提取text,null,null
// 如:varchar(100)  提取varchar, 100
func (sd *SqliteMetaData) getDataTypes(dataType string) (string, string, string) {
	matches := dataTypeRegexp.FindStringSubmatch(dataType)
	if len(matches) == 0 {
		return "", "", ""
	}
	return matches[1], matches[2], matches[3]
}

// 获取列元信息, 如列名等
func (sd *SqliteMetaData) GetColumns(tableNames ...string) ([]dbi.Column, error) {

	columns := make([]dbi.Column, 0)

	for i := 0; i < len(tableNames); i++ {
		tableName := tableNames[i]
		_, res, err := sd.dc.Query(fmt.Sprintf("PRAGMA table_info(%s)", tableName))
		if err != nil {
			logx.Error("获取数据库表字段结构出错", err.Error())
			continue
		}
		for _, re := range res {
			// 去掉默认值的引号
			defaultValue := cast.ToString(re["dflt_value"])
			if strings.Contains(defaultValue, "'") {
				defaultValue = strings.ReplaceAll(defaultValue, "'", "")
			}

			column := dbi.Column{
				TableName:     tableName,
				ColumnName:    cast.ToString(re["name"]),
				ColumnComment: "",
				Nullable:      cast.ToInt(re["notnull"]) != 1,
				IsPrimaryKey:  cast.ToInt(re["pk"]) == 1,
				IsIdentity:    cast.ToInt(re["pk"]) == 1,
				ColumnDefault: defaultValue,
				NumScale:      0,
			}

			// 切割类型和长度，如果长度内有逗号，则说明是decimal类型
			columnType := cast.ToString(re["type"])
			dataType, length, scale := sd.getDataTypes(columnType)
			if scale != "0" && scale != "" {
				column.NumPrecision = cast.ToInt(length)
				column.NumScale = cast.ToInt(scale)
				column.CharMaxLength = 0
			} else {
				column.CharMaxLength = cast.ToInt(length)
			}
			column.DataType = dbi.ColumnDataType(dataType)

			sd.FixColumn(&column)

			columns = append(columns, column)
		}
	}
	return columns, nil
}

func (sd *SqliteMetaData) FixColumn(column *dbi.Column) {
}

func (sd *SqliteMetaData) GetPrimaryKey(tableName string) (string, error) {
	_, res, err := sd.dc.Query(fmt.Sprintf("PRAGMA table_info(%s)", tableName))
	if err != nil {
		return "", err
	}
	for _, re := range res {
		if cast.ToInt(re["pk"]) == 1 {
			return cast.ToString(re["name"]), nil
		}
	}

	return "", errors.New("不存在主键")
}

// 解析索引创建语句以获取字段信息
func extractIndexFields(indexSQL string) string {
	// 使用正则表达式提取字段信息
	re := regexp.MustCompile(`\((.*?)\)`)
	match := re.FindStringSubmatch(indexSQL)
	if len(match) > 1 {
		fields := strings.Split(match[1], ",")
		for i, field := range fields {
			// 去除空格
			fields[i] = strings.TrimSpace(field)
		}
		return strings.Join(fields, ",")
	}
	return ""
}

// 获取表索引信息
func (sd *SqliteMetaData) GetTableIndex(tableName string) ([]dbi.Index, error) {
	_, res, err := sd.dc.Query(fmt.Sprintf(dbi.GetLocalSql(SQLITE_META_FILE, SQLITE_INDEX_INFO_KEY), tableName))
	if err != nil {
		return nil, err
	}

	indexs := make([]dbi.Index, 0)
	for _, re := range res {
		indexSql := cast.ToString(re["indexSql"])
		isUnique := strings.Contains(indexSql, "CREATE UNIQUE INDEX")

		indexs = append(indexs, dbi.Index{
			IndexName:    cast.ToString(re["indexName"]),
			ColumnName:   extractIndexFields(indexSql),
			IndexType:    cast.ToString(re["indexType"]),
			IndexComment: cast.ToString(re["indexComment"]),
			IsUnique:     isUnique,
			SeqInIndex:   1,
			IsPrimaryKey: false,
		})
	}
	// 把查询结果以索引名分组，索引字段以逗号连接
	return indexs, nil
}

// 获取建索引ddl
func (sd *SqliteMetaData) GenerateIndexDDL(indexs []dbi.Index, tableInfo dbi.Table) []string {
	meta := sd.dc.GetMetaData()
	sqls := make([]string, 0)
	for _, index := range indexs {
		unique := ""
		if index.IsUnique {
			unique = "unique"
		}
		// 取出列名，添加引号
		cols := strings.Split(index.ColumnName, ",")
		colNames := make([]string, len(cols))
		for i, name := range cols {
			colNames[i] = meta.QuoteIdentifier(name)
		}
		// 创建前尝试删除
		sqls = append(sqls, fmt.Sprintf("DROP INDEX IF EXISTS \"%s\"", index.IndexName))

		sqlTmp := "CREATE %s INDEX %s ON \"%s\" (%s) "
		sqls = append(sqls, fmt.Sprintf(sqlTmp, unique, index.IndexName, tableInfo.TableName, strings.Join(colNames, ",")))
	}
	return sqls
}

func (sd *SqliteMetaData) genColumnBasicSql(column dbi.Column) string {
	incr := ""
	if column.IsIdentity {
		incr = " AUTOINCREMENT"
	}

	nullAble := ""
	if !column.Nullable {
		nullAble = " NOT NULL"
	}

	quoteColumnName := sd.dc.GetMetaData().QuoteIdentifier(column.ColumnName)

	// 如果是主键，则直接返回，不判断默认值
	if column.IsPrimaryKey {
		return fmt.Sprintf(" %s integer PRIMARY KEY%s%s", quoteColumnName, incr, nullAble)
	}

	defVal := "" // 默认值需要判断引号，如函数是不需要引号的 // 为了防止跨源函数不支持 当默认值是函数时，不需要设置默认值
	if column.ColumnDefault != "" && !strings.Contains(column.ColumnDefault, "(") {
		// 哪些字段类型默认值需要加引号
		mark := false
		if collx.ArrayAnyMatches([]string{"char", "text", "date", "time", "lob"}, strings.ToLower(string(column.DataType))) {
			// 当数据类型是日期时间，默认值是日期时间函数时，默认值不需要引号
			if collx.ArrayAnyMatches([]string{"date", "time"}, strings.ToLower(string(column.DataType))) &&
				collx.ArrayAnyMatches([]string{"DATE", "TIME"}, strings.ToUpper(column.ColumnDefault)) {
				mark = false
			} else {
				mark = true
			}
		}
		if mark {
			defVal = fmt.Sprintf(" DEFAULT '%s'", column.ColumnDefault)
		} else {
			defVal = fmt.Sprintf(" DEFAULT %s", column.ColumnDefault)
		}
	}

	return fmt.Sprintf(" %s %s%s%s", quoteColumnName, column.GetColumnType(), nullAble, defVal)
}

// 获取建表ddl
func (sd *SqliteMetaData) GenerateTableDDL(columns []dbi.Column, tableInfo dbi.Table, dropBeforeCreate bool) []string {
	sqlArr := make([]string, 0)
	tbName := sd.dc.GetMetaData().QuoteIdentifier(tableInfo.TableName)
	if dropBeforeCreate {
		sqlArr = append(sqlArr, fmt.Sprintf("DROP TABLE IF EXISTS %s", tbName))
	}
	// 组装建表语句
	createSql := fmt.Sprintf("CREATE TABLE %s (\n", tbName)
	fields := make([]string, 0)

	// 把通用类型转换为达梦类型
	for _, column := range columns {
		fields = append(fields, sd.genColumnBasicSql(column))
	}
	createSql += strings.Join(fields, ",\n")
	createSql += "\n)"

	sqlArr = append(sqlArr, createSql)

	return sqlArr
}

// 获取建表ddl
func (sd *SqliteMetaData) GetTableDDL(tableName string, dropBeforeCreate bool) (string, error) {
	var builder strings.Builder

	if dropBeforeCreate {
		builder.WriteString(fmt.Sprintf("DROP TABLE IF EXISTS %s; \n\n", tableName))
	}

	_, res, err := sd.dc.Query("select sql from sqlite_master WHERE tbl_name=? order by type desc", tableName)
	if err != nil {
		return "", err
	}

	for _, re := range res {
		builder.WriteString(cast.ToString(re["sql"]) + "; \n\n")
	}

	return builder.String(), nil
}

func (sd *SqliteMetaData) GetSchemas() ([]string, error) {
	return nil, nil
}

func (sd *SqliteMetaData) GetDataConverter() dbi.DataConverter {
	return converter
}

func (sd *SqliteMetaData) BeforeDumpInsert(writer io.Writer, tableName string) {
}
func (sd *SqliteMetaData) AfterDumpInsert(writer io.Writer, tableName string, columns []dbi.Column) {
}

var (
	// 数字类型
	numberRegexp = regexp.MustCompile(`(?i)int|double|float|number|decimal|byte|bit|real`)
	// 日期时间类型
	datetimeRegexp = regexp.MustCompile(`(?i)datetime`)

	dataTypeRegexp = regexp.MustCompile(`(\w+)\((\d*),?(\d*)\)`)

	converter = new(DataConverter)

	//  sqlite数据类型 映射 公共数据类型
	commonColumnTypeMap = map[string]dbi.ColumnDataType{
		"int":               dbi.CommonTypeInt,
		"integer":           dbi.CommonTypeInt,
		"tinyint":           dbi.CommonTypeTinyint,
		"smallint":          dbi.CommonTypeSmallint,
		"mediumint":         dbi.CommonTypeSmallint,
		"bigint":            dbi.CommonTypeBigint,
		"int2":              dbi.CommonTypeInt,
		"int8":              dbi.CommonTypeInt,
		"character":         dbi.CommonTypeChar,
		"varchar":           dbi.CommonTypeVarchar,
		"varying character": dbi.CommonTypeVarchar,
		"nchar":             dbi.CommonTypeChar,
		"native character":  dbi.CommonTypeVarchar,
		"nvarchar":          dbi.CommonTypeVarchar,
		"text":              dbi.CommonTypeText,
		"clob":              dbi.CommonTypeBlob,
		"blob":              dbi.CommonTypeBlob,
		"real":              dbi.CommonTypeNumber,
		"double":            dbi.CommonTypeNumber,
		"double precision":  dbi.CommonTypeNumber,
		"float":             dbi.CommonTypeNumber,
		"numeric":           dbi.CommonTypeNumber,
		"decimal":           dbi.CommonTypeNumber,
		"boolean":           dbi.CommonTypeTinyint,
		"date":              dbi.CommonTypeDate,
		"datetime":          dbi.CommonTypeDatetime,
	}

	//  公共数据类型 映射 sqlite数据类型
	sqliteColumnTypeMap = map[dbi.ColumnDataType]string{
		dbi.CommonTypeVarchar:    "nvarchar",
		dbi.CommonTypeChar:       "nchar",
		dbi.CommonTypeText:       "text",
		dbi.CommonTypeBlob:       "blob",
		dbi.CommonTypeLongblob:   "blob",
		dbi.CommonTypeLongtext:   "text",
		dbi.CommonTypeBinary:     "text",
		dbi.CommonTypeMediumblob: "blob",
		dbi.CommonTypeMediumtext: "text",
		dbi.CommonTypeVarbinary:  "text",
		dbi.CommonTypeInt:        "int",
		dbi.CommonTypeSmallint:   "smallint",
		dbi.CommonTypeTinyint:    "tinyint",
		dbi.CommonTypeNumber:     "number",
		dbi.CommonTypeBigint:     "bigint",
		dbi.CommonTypeDatetime:   "datetime",
		dbi.CommonTypeDate:       "date",
		dbi.CommonTypeTime:       "datetime",
		dbi.CommonTypeTimestamp:  "datetime",
		dbi.CommonTypeEnum:       "nvarchar(2000)",
		dbi.CommonTypeJSON:       "nvarchar(2000)",
	}
)

type DataConverter struct {
}

func (dc *DataConverter) GetDataType(dbColumnType string) dbi.DataType {
	if numberRegexp.MatchString(dbColumnType) {
		return dbi.DataTypeNumber
	}
	if datetimeRegexp.MatchString(dbColumnType) {
		return dbi.DataTypeDateTime
	}
	return dbi.DataTypeString
}

func (dc *DataConverter) FormatData(dbColumnValue any, dataType dbi.DataType) string {
	str := anyx.ToString(dbColumnValue)
	switch dataType {
	case dbi.DataTypeDateTime: // "2024-01-02T22:08:22.275697+08:00"
		// 尝试用时间格式解析
		res, err := time.Parse(time.DateTime, str)
		if err == nil {
			return str
		}
		res, _ = time.Parse(time.RFC3339, str)
		return res.Format(time.DateTime)
	case dbi.DataTypeDate: // "2024-01-02T00:00:00+08:00"
		// 尝试用时间格式解析
		res, err := time.Parse(time.DateOnly, str)
		if err == nil {
			return str
		}
		res, _ = time.Parse(time.RFC3339, str)
		return res.Format(time.DateOnly)
	case dbi.DataTypeTime: // "0000-01-01T22:08:22.275688+08:00"
		// 尝试用时间格式解析
		res, err := time.Parse(time.TimeOnly, str)
		if err == nil {
			return str
		}
		res, _ = time.Parse(time.RFC3339, str)
		return res.Format(time.TimeOnly)
	}
	return str
}

func (dc *DataConverter) ParseData(dbColumnValue any, dataType dbi.DataType) any {
	return dbColumnValue
}

func (dc *DataConverter) WrapValue(dbColumnValue any, dataType dbi.DataType) string {
	if dbColumnValue == nil {
		return "NULL"
	}
	switch dataType {
	case dbi.DataTypeNumber:
		return fmt.Sprintf("%v", dbColumnValue)
	case dbi.DataTypeString:
		val := fmt.Sprintf("%v", dbColumnValue)
		// 转义单引号
		val = strings.Replace(val, `'`, `''`, -1)
		val = strings.Replace(val, `\''`, `\'`, -1)
		// 转义换行符
		val = strings.Replace(val, "\n", "\\n", -1)
		return fmt.Sprintf("'%s'", val)
	case dbi.DataTypeDate, dbi.DataTypeDateTime, dbi.DataTypeTime:
		return fmt.Sprintf("'%s'", dc.FormatData(dbColumnValue, dataType))
	}
	return fmt.Sprintf("'%s'", dbColumnValue)
}