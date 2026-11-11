import { Verifier } from '@pact-foundation/pact'
import { resolve } from 'node:path'
import { createServer } from 'node:http'
import { randomUUID } from 'node:crypto'
import assert from 'node:assert/strict'
import './provenance.mjs'

const base='http://synkro-sales-api:8080'
const admin=process.env.ADMIN_TOKEN, service=process.env.SERVICE_TOKEN
assert.ok(admin && service,'Disposable provider credentials are required')
const options={provider:'SalesAPI',pactUrls:[resolve('contracts/SalesPortal-SalesAPI.json')],logLevel:'error'}
const states={
  'a numbered pact sale exists':async()=>{
    const controller=new AbortController(),timer=setTimeout(()=>controller.abort(),20000)
    try {
      const response=await fetch(base+'/api/v1/sales',{method:'POST',signal:controller.signal,
        headers:{Authorization:`Bearer ${service}`,'Content-Type':'application/json','Idempotency-Key':randomUUID()},
        body:JSON.stringify({customerId:'550e8400-e29b-41d4-a716-446655440001',createdBy:'550e8400-e29b-41d4-a716-446655440002',
          details:[{productId:'550e8400-e29b-41d4-a716-446655440004',quantity:1,unitPriceCents:100}]})})
      assert.equal(response.status,201)
      return {saleId:(await response.json()).saleId}
    } finally {clearTimeout(timer);controller.abort()}
  },
  'a legacy unnumbered pact sale exists':async()=>({saleId:'550e8400-e29b-41d4-a716-446655440099'}),
}
let wrongRequests=0
const wrong=createServer((req,res)=>{wrongRequests++;req.resume();res.setHeader('Content-Type','application/json');res.end('{"wrongContract":true}')})
await new Promise(r=>wrong.listen(0,'127.0.0.1',r))
try {
  await assert.rejects(()=>new Verifier({...options,providerBaseUrl:'http://127.0.0.1:'+wrong.address().port,
    stateHandlers:Object.fromEntries(Object.keys(states).map(s=>[s,async()=>({saleId:'550e8400-e29b-41d4-a716-446655440099'})]))}).verifyProvider())
  assert.ok(wrongRequests>=2,'Negative control must reach the incompatible HTTP provider, not fail during setup')
  console.log('Pact negative control: incompatible Sales provider rejected')
} finally {await new Promise(r=>wrong.close(r))}
await new Verifier({...options,providerBaseUrl:base,stateHandlers:states,
  requestFilter:(req,res,next)=>{req.headers.authorization=`Bearer ${admin}`;next()}}).verifyProvider()
console.log('Pact provider: generated SalesPortal contract verified against real Sales API/MongoDB')
