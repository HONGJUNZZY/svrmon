export const hasSystemPower = (ipmi) => Boolean(ipmi?.powerAvailable || ipmi?.powerWatts > 0)
export function powerSourceLabel(ipmi) {
  return ({ asus_psu_input: 'CRPS / PSU 输入功率合计（ASUS BMC 实时）', redfish_psu_input: 'CRPS / PSU 输入功率合计（Redfish）', ipmi_psu_input: 'CRPS / PSU 输入功率合计（IPMI）', ipmi_system: '整机功耗传感器（IPMI）', dcmi: 'DCMI 实时功耗' })[ipmi?.powerSource] || '整机功耗（旧版来源未标记）'
}
