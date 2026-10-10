// alert_episodes.go —— D-168：飞书推送会话式 episode 制。
// 键=alerttext.AlertDedupKey（与 insertAlert 30min 去重同归一化）；态机存 app_config
// JSON 单键（cfgPaperNetHist 先例，零 schema）。惰性关闭：键 >24h 无新行→closed→
// 再发=新集推一次；open 未 ack 且距上次推 >24h 仍有新行→惰性提醒一条（reminded 单次）。
package pgstore

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"arb/internal/alerttext"
)

// cfgAlertEpisodes episode 状态键（app_config value=alertEpisodeDoc JSON）。
const cfgAlertEpisodes = "ARB_ALERT_EPISODES"

const (
	episodeOpen  = "open"
	episodeAcked = "acked"
)

// Episode 惰性关闭/提醒阈值（包级 var 供测试缩短）。
var (
	episodeCloseAfter  = 24 * time.Hour
	episodeRemindAfter = 24 * time.Hour
	episodeRetain      = 7 * 24 * time.Hour
)

// AlertEpisodeAction 推送闸裁决。
type AlertEpisodeAction int

const (
	EpisodeSuppress AlertEpisodeAction = iota // 抑制推（last_fired 已续期）
	EpisodePush                               // 新集/旧集已闭 → 推
	EpisodeRemind                             // open 未 ack 超阈值 → 「仍待确认」提醒
)

// alertEpisode 单集记录。
type alertEpisode struct {
	State      string    `json:"state"`
	FirstFired time.Time `json:"first_fired"`
	LastFired  time.Time `json:"last_fired"`
	LastPush   time.Time `json:"last_push"`
	PushMsgID  string    `json:"push_msg_id,omitempty"`
	Reminded   bool      `json:"reminded,omitempty"`
}

type alertEpisodeDoc struct {
	Episodes map[string]*alertEpisode `json:"episodes"`
}

// episodeMu 序列化 load-modify-write（feishuPush 4 worker 并发；单进程内锁足够——
// 生产唯一推送进程=core，跨进程写场景不存在）。
var episodeMu sync.Mutex

func (s *Store) loadEpisodes(ctx context.Context) (*alertEpisodeDoc, error) {
	v, ok, err := s.AppConfigString(ctx, cfgAlertEpisodes)
	if err != nil || !ok {
		return &alertEpisodeDoc{Episodes: map[string]*alertEpisode{}}, err
	}
	var doc alertEpisodeDoc
	if err := json.Unmarshal([]byte(v), &doc); err != nil {
		return &alertEpisodeDoc{Episodes: map[string]*alertEpisode{}}, fmt.Errorf("alert episodes decode: %w", err)
	}
	if doc.Episodes == nil {
		doc.Episodes = map[string]*alertEpisode{}
	}
	return &doc, nil
}

// saveEpisodes 落库前修剪（closed/acked 且超保留期的集清出，防 app_config 行膨胀）。
func (s *Store) saveEpisodes(ctx context.Context, doc *alertEpisodeDoc) error {
	cut := time.Now().Add(-episodeRetain)
	for k, e := range doc.Episodes {
		if e != nil && e.State == episodeAcked && e.LastFired.Before(cut) {
			delete(doc.Episodes, k)
		}
	}
	buf, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("alert episodes encode: %w", err)
	}
	return s.UpsertAppConfig(ctx, cfgAlertEpisodes, string(buf), "D-168 飞书告警 episode 态机（内部状态键）")
}

// AlertEpisodeGate 推送闸裁决：新集/旧集已闭→Push（开集记账）；open/acked 续期抑制；
// open 未 ack 超提醒阈→Remind 一次。返回 dedup 键供 push_msg_id 对位。
func (s *Store) AlertEpisodeGate(ctx context.Context, level, message string, now time.Time) (AlertEpisodeAction, string, error) {
	episodeMu.Lock()
	defer episodeMu.Unlock()
	key := alerttext.AlertDedupKey(level, message)
	doc, err := s.loadEpisodes(ctx)
	if err != nil {
		return EpisodeSuppress, "", err
	}
	e := doc.Episodes[key]
	// 惰性关闭：键 >24h 无新行 → 旧集判闭，本条开新集。
	if e != nil && now.Sub(e.LastFired) > episodeCloseAfter {
		e = nil
	}
	act := EpisodeSuppress
	switch {
	case e == nil:
		e = &alertEpisode{State: episodeOpen, FirstFired: now, LastFired: now, LastPush: now}
		doc.Episodes[key] = e
		act = EpisodePush
	default:
		e.LastFired = now
		if e.State == episodeOpen && !e.Reminded && now.Sub(e.LastPush) > episodeRemindAfter {
			e.Reminded = true
			e.LastPush = now
			act = EpisodeRemind
		}
	}
	if err := s.saveEpisodes(ctx, doc); err != nil {
		return EpisodeSuppress, "", err
	}
	return act, key, nil
}

// RecordAlertEpisodePushMsg 推送成功后回写 message_id（引用回复 ack 对位）。
func (s *Store) RecordAlertEpisodePushMsg(ctx context.Context, key, msgID string) error {
	episodeMu.Lock()
	defer episodeMu.Unlock()
	doc, err := s.loadEpisodes(ctx)
	if err != nil {
		return err
	}
	if e := doc.Episodes[key]; e != nil {
		e.PushMsgID = msgID
		return s.saveEpisodes(ctx, doc)
	}
	return nil
}

// AckAllAlertEpisodes 裸词 ack：全部 open→acked，返回条数。
func (s *Store) AckAllAlertEpisodes(ctx context.Context) (int, error) {
	episodeMu.Lock()
	defer episodeMu.Unlock()
	doc, err := s.loadEpisodes(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range doc.Episodes {
		if e != nil && e.State == episodeOpen {
			e.State = episodeAcked
			n++
		}
	}
	if n > 0 {
		if err := s.saveEpisodes(ctx, doc); err != nil {
			return 0, err
		}
	}
	return n, nil
}

// AckAlertEpisodeByPushMsg 引用回复 ack：parent_id 对位单集，返回键+是否命中。
func (s *Store) AckAlertEpisodeByPushMsg(ctx context.Context, msgID string) (string, bool, error) {
	episodeMu.Lock()
	defer episodeMu.Unlock()
	doc, err := s.loadEpisodes(ctx)
	if err != nil {
		return "", false, err
	}
	for k, e := range doc.Episodes {
		if e != nil && e.PushMsgID == msgID {
			if e.State == episodeOpen {
				e.State = episodeAcked
				if err := s.saveEpisodes(ctx, doc); err != nil {
					return "", false, err
				}
			}
			return k, true, nil
		}
	}
	return "", false, nil
}

// AckAlertEpisodeByAlertID 面板 AckAlert 反查：alert 行 message→dedup 键→ack 集。
func (s *Store) AckAlertEpisodeByAlertID(ctx context.Context, id int64) error {
	var level, msg string
	if err := s.pool.QueryRow(ctx, `SELECT level, message FROM alerts WHERE id = $1`, id).Scan(&level, &msg); err != nil {
		return fmt.Errorf("ack episode by alert: %w", err)
	}
	episodeMu.Lock()
	defer episodeMu.Unlock()
	doc, err := s.loadEpisodes(ctx)
	if err != nil {
		return err
	}
	key := alerttext.AlertDedupKey(level, msg)
	if e := doc.Episodes[key]; e != nil && e.State == episodeOpen {
		e.State = episodeAcked
		return s.saveEpisodes(ctx, doc)
	}
	return nil
}

// AckUnackedAlertsByKey 飞书 ack 同步 alerts.acked（铃铛清）：未读行 Go 侧重算键
// 对位（同 recentAlertDuplicate 归一化先例）。
func (s *Store) AckUnackedAlertsByKey(ctx context.Context, key string) (int, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, level, message FROM alerts WHERE acked = false`)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		var level, msg string
		if err := rows.Scan(&id, &level, &msg); err != nil {
			rows.Close()
			return 0, err
		}
		if alerttext.AlertDedupKey(level, msg) == key {
			ids = append(ids, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	if _, err := s.pool.Exec(ctx, `UPDATE alerts SET acked = true WHERE id = ANY($1)`, ids); err != nil {
		return 0, fmt.Errorf("ack alerts by key: %w", err)
	}
	return len(ids), nil
}
