// Used only after require_disposable_database by contract-tests.sh, never against the persistent installation.
const admin=db.getSiblingDB('admin');
if(!admin.auth('admin',process.env.CI_ADMIN_PASSWORD))throw Error('Disposable fixture authentication failed');
db.getSiblingDB('sales').sale.insertOne({_id:'550e8400-e29b-41d4-a716-446655440099',
  customerId:'550e8400-e29b-41d4-a716-446655440001',createdBy:'550e8400-e29b-41d4-a716-446655440002',
  date:new Date('2026-11-10T00:00:00Z'),totalCents:NumberLong(100),active:true,idempotencyKey:'pact-legacy-fixture-0001',
  details:[{detailId:'550e8400-e29b-41d4-a716-446655440003',productId:'550e8400-e29b-41d4-a716-446655440004',
    quantity:NumberInt(1),unitPriceCents:NumberLong(100),subtotalCents:NumberLong(100),active:true}]});
