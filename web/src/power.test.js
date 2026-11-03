import test from 'node:test'
import assert from 'node:assert/strict'
import { hasSystemPower, powerSourceLabel } from './power.js'

test('known zero watts is distinct from unavailable power and old agents remain supported', () => {
  assert.equal(hasSystemPower({ powerWatts: 0, powerAvailable: true }), true)
  assert.equal(hasSystemPower({ powerWatts: 0 }), false)
  assert.equal(hasSystemPower({ powerWatts: 334 }), true)
  assert.equal(hasSystemPower(null), false)
})

test('PSU AC input and DCMI remain visibly distinct sources', () => {
  assert.match(powerSourceLabel({ powerSource: 'asus_psu_input' }), /输入功率合计.*ASUS BMC 实时/)
  assert.match(powerSourceLabel({ powerSource: 'redfish_psu_input' }), /输入功率合计.*Redfish/)
  assert.match(powerSourceLabel({ powerSource: 'dcmi' }), /DCMI/)
  assert.match(powerSourceLabel({ powerWatts: 334 }), /来源未标记/)
})
