-- ============================================================
-- v0.7.2: 复刻 mattermost-pre.modules 表到 im_pre。
--
-- mattermost csesapi /modules/getAll 直接读这张表（label/url/id 是给前端
-- 渲染"模块入口"卡片用的：会议聊天 / 审批 / 任务 / 成果导向 / 切换公司 /
-- 文档六个固定槽位）。前端 ImApiAdapter 需要把 /modules/getAll 路由到我们，
-- 数据完全 1:1 照搬，避免影响前端渲染逻辑。
--
-- 表结构与 mattermost 一致：name 是 PK，label/url/id 三个 nullable 字段。
-- 没有 created_at / updated_at —— mattermost 也没有，照搬。
-- ============================================================

CREATE TABLE modules (
    name  VARCHAR(100) PRIMARY KEY,
    label VARCHAR(100),
    url   TEXT,
    id    VARCHAR(64)
);

INSERT INTO modules (name, label, url, id) VALUES
    ('meeting',   '会议聊天', 'http://picture.jinqidongli.com/64915482596513266737afbe/691185d7702ba1671ba81f37.png', 'qonozjdz3if5fy3qbnubzi9cqc'),
    ('approval',  '审批',     'http://picture.jinqidongli.com/64915482596513266737afbe/691185d9702ba1671ba81f41.png', '1tgke46ye3n19rt5h21gsahgbca'),
    ('company',   '切换公司', 'http://picture.jinqidongli.com/64915482596513266737afbe/691185d8702ba1671ba81f3b.png', '1tgke46ye3n19rt5h21gsahgbc2'),
    ('orient',    '成果导向', 'http://picture.jinqidongli.com/64915482596513266737afbe/691185d7702ba1671ba81f35.png', '1tgke46ye3n19rt5h7gsahgbca'),
    ('task',      '任务',     'http://picture.jinqidongli.com/64915482596513266737afbe/691185d8702ba1671ba81f3d.png', '1tgke46ye3n19rt5h21gsahgbcb'),
    ('doc_nodes', '文档',     'http://picture.jinqidongli.com/64915482596513266737afbe/691185d9702ba1671ba81f43.png', 'qonozjdz3if5fy3qbnubzi9cqe');
