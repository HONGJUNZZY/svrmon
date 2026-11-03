# 多 GPU 与整机输入功耗

## GPU 页面

总览显示全部 GPU 的真实编号、型号、利用率、显存占用、温度和功耗，不再截取前 8 张卡或前 4 张温度。单机 GPU 页按 UUID 分别保存最近约 8 分钟曲线，显示每张卡的利用率和显存占用曲线，并提供逐卡温度、功耗对比。GPU 更换或枚举顺序改变不会把两张卡的曲线混在一起。

显存占用率按 `memory.used / memory.total` 计算；`nvidia-smi utilization.memory` 表示显存带宽活动率，作为另一个指标显示。

原有 SQLite 历史数据仅保存多卡利用率均值、最高温度和功耗合计。历史页明确标识这些汇总指标；逐卡实时曲线从当前浏览器连接时开始积累，不会从历史均值伪造逐卡数据。

## 整机功耗取值

1. 配置 `asusPower` 时，读取 ASUS BMC 实时 PSU 接口的 `ACPIN`，逐路求和；否则配置 Redfish 时，汇总 `PowerSupplies[].PowerInputWatts`，同时上报各路输入值。
2. 若标准 IPMI 中存在完整的 PSU/CRPS 输入功率传感器，则对各路输入求和，同一路电源的别名不会重复累计。
3. 明确的整机功耗传感器，或 `ipmitool dcmi power reading` 的 `Instantaneous power reading` 可作为回退来源。
4. 不使用输出功率、额定容量、CPU/GPU 功耗或任意瓦特传感器的最大值充当整机功耗。不完整的输入数据不以部分电源之和冒充总量。

AC 输入包括电源转换损耗，因此会高于电源 DC 输出之和。页面同时展示取值来源、输入明细和采样时间；缺失读数显示为不可用，已测得的 0 W 则正常显示。

## 可选 Redfish 配置

部分主板的标准 IPMI 传感器列表没有 PSU 输入读数，但 BMC 的 Redfish Power 资源提供该数据。可在 Agent 的 `ipmi` 配置中添加：

```json
{
  "ipmi": {
    "enable": true,
    "path": "ipmitool",
    "sensorSec": 30,
    "selSec": 60,
    "redfish": {
      "url": "https://bmc.example/redfish/v1/Chassis/Self/Power",
      "username": "monitor",
      "password": "由管理员填入BMC只读账号凭据",
      "certificateSHA256": "自签名BMC证书的64位SHA256指纹"
    }
  }
}
```

URL 路径取决于 BMC 实际提供的 Chassis 资源。该功能仅发送 HTTPS GET 请求，不修改电源或 BMC 设置；建议使用具备读取权限的专用账号。配置含凭据，应保持文件权限 600。

未填写证书指纹时使用系统 CA 验证。自签名证书可配置叶证书的 SHA-256 指纹，连接只信任完全匹配的证书，不是无条件跳过验证。BMC 证书更新后需同步指纹，否则会报告读取失败，并明确标记回退功耗来源。

## ASUS 固件的 Redfish 输入功耗冻结

部分 ASUS BMC（实测 Z13PE-D16 Series）会不断更新 Redfish `PowerControl.PowerConsumedWatts`，但 `PowerSupplies[].PowerInputWatts` 始终停留在旧值。HTTP 成功、采样时间更新或 ETag 不变均不能证明逐电源读数实时有效。部署时应跨多个采样周期对照原始输入值，不能只验证一次求和。

这类设备可将 `ipmi.redfish` 改为以下配置：

```json
{
  "ipmi": {
    "enable": true,
    "path": "ipmitool",
    "sensorSec": 30,
    "selSec": 60,
    "asusPower": {
      "url": "https://bmc.example",
      "username": "monitor",
      "password": "由管理员填入BMC账号凭据",
      "certificateSHA256": "自签名BMC证书的64位SHA256指纹",
      "expectedSupplies": 2
    }
  }
}
```

`url` 仅填写 HTTPS 地址，不含 API 路径。Agent 登录 `/api/session` 后复用会话，通过 GET `/api/host_inventory/host_interface_power_supp` 读取实时 AC 输入，过期时续登；不修改电源设置。`expectedSupplies` 为应返回的电源槽位数（包含明确标记为缺席的槽位），用于发现整路数据缺失。

若该接口出现缺失/非法值，或 AC 输入显著低于对应的 DC 输出，则丢弃本次整组读数，回退本地 IPMI/DCMI，并上报原因与实际来源；正常的零输入/零输出仍有效。`asusPower` 优先于 `redfish`，失败后不会再读已知冻结的 Redfish PSU 字段。不要将 DCMI 整机功耗误标为已读取的逐路 CRPS 输入。

旧版采集的恒定历史无法还原为真实功耗，升级不会改写既有历史。

## 验证与升级

```bash
go test ./internal/agent/... ./internal/hub/...
node --test web/src/gpu.test.js web/src/power.test.js
```

沿用 `deploy/build.ps1` 构建。界面和来源字段需更新 Hub；功耗采集需更新相应 Agent 并配置 BMC。仅多 GPU 曲线显示改进不要求升级已有 Agent。无需修改数据库结构，既有历史不会重新计算。
