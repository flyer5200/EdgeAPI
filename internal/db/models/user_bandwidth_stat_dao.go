package models

import (
	"github.com/TeaOSLab/EdgeAPI/internal/errors"
	"github.com/TeaOSLab/EdgeAPI/internal/goman"
	"github.com/TeaOSLab/EdgeAPI/internal/remotelogs"
	"github.com/TeaOSLab/EdgeAPI/internal/utils"
	"github.com/TeaOSLab/EdgeAPI/internal/utils/regexputils"
	"github.com/TeaOSLab/EdgeCommon/pkg/rpc/pb"
	_ "github.com/go-sql-driver/mysql"
	"github.com/iwind/TeaGo/Tea"
	"github.com/iwind/TeaGo/dbs"
	"github.com/iwind/TeaGo/maps"
	"github.com/iwind/TeaGo/rands"
	"github.com/iwind/TeaGo/types"
	timeutil "github.com/iwind/TeaGo/utils/time"
	"math"
	"strings"
	"sync"
	"time"
)

type UserBandwidthStatDAO dbs.DAO

const (
	UserBandwidthStatTablePartials = 20
)

func init() {
	dbs.OnReadyDone(func() {
		// 清理数据任务
		var ticker = time.NewTicker(time.Duration(rands.Int(24, 48)) * time.Hour)
		goman.New(func() {
			for range ticker.C {
				err := SharedUserBandwidthStatDAO.Clean(nil)
				if err != nil {
					remotelogs.Error("SharedUserBandwidthStatDAO", "clean expired data failed: "+err.Error())
				}
			}
		})
	})
}

func NewUserBandwidthStatDAO() *UserBandwidthStatDAO {
	return dbs.NewDAO(&UserBandwidthStatDAO{
		DAOObject: dbs.DAOObject{
			DB:     Tea.Env,
			Table:  "edgeUserBandwidthStats",
			Model:  new(UserBandwidthStat),
			PkName: "id",
		},
	}).(*UserBandwidthStatDAO)
}

var SharedUserBandwidthStatDAO *UserBandwidthStatDAO

func init() {
	dbs.OnReady(func() {
		SharedUserBandwidthStatDAO = NewUserBandwidthStatDAO()
	})
}

// UpdateUserBandwidth 写入数据
func (this *UserBandwidthStatDAO) UpdateUserBandwidth(tx *dbs.Tx, userId int64, regionId int64, day string, timeAt string, bytes int64) error {
	if userId <= 0 {
		// 如果用户ID不大于0，则说明服务不属于任何用户，此时不需要处理
		return nil
	}

	return this.Query(tx).
		Table(this.partialTable(userId)).
		Param("bytes", bytes).
		InsertOrUpdateQuickly(maps.Map{
			"userId":   userId,
			"regionId": regionId,
			"day":      day,
			"timeAt":   timeAt,
			"bytes":    bytes,
		}, maps.Map{
			"bytes": dbs.SQL("bytes+:bytes"),
		})
}

// FindUserPeekBandwidthInMonth 读取某月带宽峰值
// month YYYYMM
func (this *UserBandwidthStatDAO) FindUserPeekBandwidthInMonth(tx *dbs.Tx, userId int64, month string) (*UserBandwidthStat, error) {
	one, err := this.Query(tx).
		Table(this.partialTable(userId)).
		Result("MIN(id) AS id", "MIN(userId) AS userId", "day", "timeAt", "SUM(bytes) AS bytes").
		Attr("userId", userId).
		Between("day", month+"01", month+"31").
		Desc("bytes").
		Group("day").
		Group("timeAt").
		Find()
	if err != nil || one == nil {
		return nil, err
	}
	return one.(*UserBandwidthStat), nil
}

// FindPercentileBetweenDays 获取日期段内内百分位
// regionId 如果为 -1 表示没有区域的带宽；如果为 0 表示所有区域的带宽
func (this *UserBandwidthStatDAO) FindPercentileBetweenDays(tx *dbs.Tx, userId int64, regionId int64, dayFrom string, dayTo string, percentile int32) (result *UserBandwidthStat, err error) {
	if dayFrom > dayTo {
		dayFrom, dayTo = dayTo, dayFrom
	}

	if percentile <= 0 {
		percentile = 95
	}

	// 如果是100%以上，则快速返回
	if percentile >= 100 {
		var query = this.Query(tx).
			Table(this.partialTable(userId))
		if regionId > 0 {
			query.Attr("regionId", regionId)
		} else if regionId < 0 {
			query.Attr("regionId", 0)
		}
		one, err := query.
			Result("MIN(id) AS id", "MIN(userId) AS userId", "day", "timeAt", "SUM(bytes) AS bytes").
			Attr("userId", userId).
			Between("day", dayFrom, dayTo).
			Desc("bytes").
			Group("day").
			Group("timeAt").
			Find()
		if err != nil || one == nil {
			return nil, err
		}

		return one.(*UserBandwidthStat), nil
	}

	// 总数量
	var totalQuery = this.Query(tx).
		Table(this.partialTable(userId))
	if regionId > 0 {
		totalQuery.Attr("regionId", regionId)
	} else if regionId < 0 {
		totalQuery.Attr("regionId", 0)
	}
	total, err := totalQuery.
		Attr("userId", userId).
		Between("day", dayFrom, dayTo).
		CountAttr("DISTINCT day, timeAt")
	if err != nil {
		return nil, err
	}
	if total == 0 {
		return nil, nil
	}

	var offset int64

	if total > 1 {
		offset = int64(math.Ceil(float64(total) * float64(100-percentile) / 100))
	}

	// 查询 nth 位置
	var query = this.Query(tx).
		Table(this.partialTable(userId))
	if regionId > 0 {
		query.Attr("regionId", regionId)
	} else if regionId < 0 {
		query.Attr("regionId", 0)
	}
	one, err := query.
		Result("MIN(id) AS id", "MIN(userId) AS userId", "day", "timeAt", "SUM(bytes) AS bytes").
		Attr("userId", userId).
		Between("day", dayFrom, dayTo).
		Desc("bytes").
		Group("day").
		Group("timeAt").
		Offset(offset).
		Find()
	if err != nil || one == nil {
		return nil, err
	}

	return one.(*UserBandwidthStat), nil
}

// FindUserPeekBandwidthInDay 读取某日带宽峰值
// day YYYYMMDD
func (this *UserBandwidthStatDAO) FindUserPeekBandwidthInDay(tx *dbs.Tx, userId int64, day string) (*UserBandwidthStat, error) {
	one, err := this.Query(tx).
		Table(this.partialTable(userId)).
		Result("MIN(id) AS id", "MIN(userId) AS userId", "MIN(day) AS day", "timeAt", "SUM(bytes) AS bytes").
		Attr("userId", userId).
		Attr("day", day).
		Desc("bytes").
		Group("timeAt").
		Find()
	if err != nil || one == nil {
		return nil, err
	}
	return one.(*UserBandwidthStat), nil
}

// FindUserBandwidthStatsBetweenDays 查找日期段内的带宽峰值
// dayFrom YYYYMMDD
// dayTo YYYYMMDD
func (this *UserBandwidthStatDAO) FindUserBandwidthStatsBetweenDays(tx *dbs.Tx, userId int64, regionId int64, dayFrom string, dayTo string) (result []*pb.FindDailyServerBandwidthStatsBetweenDaysResponse_Stat, err error) {
	if userId <= 0 {
		return nil, nil
	}

	if !regexputils.YYYYMMDD.MatchString(dayFrom) {
		return nil, errors.New("invalid dayFrom '" + dayFrom + "'")
	}
	if !regexputils.YYYYMMDD.MatchString(dayTo) {
		return nil, errors.New("invalid dayTo '" + dayTo + "'")
	}

	if dayFrom > dayTo {
		dayFrom, dayTo = dayTo, dayFrom
	}

	var query = this.Query(tx).
		Table(this.partialTable(userId))
	if regionId > 0 {
		query.Attr("regionId", regionId)
	}
	ones, _, err := query.
		Result("SUM(bytes) AS bytes", "day", "timeAt").
		Attr("userId", userId).
		Between("day", dayFrom, dayTo).
		Group("day").
		Group("timeAt").
		FindOnes()
	if err != nil {
		return nil, err
	}

	var m = map[string]*pb.FindDailyServerBandwidthStatsBetweenDaysResponse_Stat{}
	for _, one := range ones {
		var day = one.GetString("day")
		var bytes = one.GetInt64("bytes")
		var timeAt = one.GetString("timeAt")
		var key = day + "@" + timeAt

		m[key] = &pb.FindDailyServerBandwidthStatsBetweenDaysResponse_Stat{
			Bytes:  bytes,
			Bits:   bytes * 8,
			Day:    day,
			TimeAt: timeAt,
		}
	}

	allDays, err := utils.RangeDays(dayFrom, dayTo)
	if err != nil {
		return nil, err
	}

	dayTimes, err := utils.Range24HourTimes(5)
	if err != nil {
		return nil, err
	}

	// 截止到当前时间
	var currentTime = timeutil.Format("Ymd@Hi")

	for _, day := range allDays {
		for _, timeAt := range dayTimes {
			var key = day + "@" + timeAt
			if key >= currentTime {
				break
			}

			stat, ok := m[key]
			if ok {
				result = append(result, stat)
			} else {
				result = append(result, &pb.FindDailyServerBandwidthStatsBetweenDaysResponse_Stat{
					Day:    day,
					TimeAt: timeAt,
				})
			}
		}
	}

	return result, nil
}

// FindDistinctUserIds 获取所有有带宽的用户ID
// dayFrom YYYYMMDD
// dayTo YYYYMMDD
func (this *UserBandwidthStatDAO) FindDistinctUserIds(tx *dbs.Tx, dayFrom string, dayTo string) (userIds []int64, err error) {
	dayFrom = strings.ReplaceAll(dayFrom, "-", "")
	dayTo = strings.ReplaceAll(dayTo, "-", "")

	err = this.runBatch(func(table string, locker *sync.Mutex) error {
		ones, err := this.Query(tx).
			Table(table).
			Between("day", dayFrom, dayTo).
			Result("DISTINCT userId").
			FindAll()
		if err != nil {
			return err
		}

		for _, one := range ones {
			locker.Lock()
			var userId = int64(one.(*UserBandwidthStat).UserId)
			if userId > 0 {
				userIds = append(userIds, userId)
			}
			locker.Unlock()
		}
		return nil
	})
	return
}

// Clean 清理过期数据
func (this *UserBandwidthStatDAO) Clean(tx *dbs.Tx) error {
	var day = timeutil.Format("Ymd", time.Now().AddDate(0, 0, -100)) // 保留大约3个月的数据
	return this.runBatch(func(table string, locker *sync.Mutex) error {
		_, err := this.Query(tx).
			Table(table).
			Lt("day", day).
			Delete()
		return err
	})
}

// 批量执行
func (this *UserBandwidthStatDAO) runBatch(f func(table string, locker *sync.Mutex) error) error {
	var locker = &sync.Mutex{}
	var wg = sync.WaitGroup{}
	wg.Add(UserBandwidthStatTablePartials)
	var resultErr error
	for i := 0; i < UserBandwidthStatTablePartials; i++ {
		var table = this.partialTable(int64(i))
		go func(table string) {
			defer wg.Done()

			err := f(table, locker)
			if err != nil {
				resultErr = err
			}
		}(table)
	}
	wg.Wait()
	return resultErr
}

// 获取分区表
func (this *UserBandwidthStatDAO) partialTable(userId int64) string {
	return this.Table + "_" + types.String(userId%int64(UserBandwidthStatTablePartials))
}
