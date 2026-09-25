package daed

// Handwritten GraphQL documents. daed has no subscriptions; every operation
// is a plain POST to /graphql. Field sets mirror schema.graphql (the frozen
// SDL of daed v2.1.1, the final release).

const qNumberUsers = `query NumberUsers { numberUsers }`

const qHealthCheck = `query HealthCheck { healthCheck }`

const qToken = `query Token($username: String!, $password: String!) {
	token(username: $username, password: $password)
}`

const qCreateUser = `mutation CreateUser($username: String!, $password: String!) {
	createUser(username: $username, password: $password)
}`

const qGeneral = `query General {
	general {
		dae { running modified version }
	}
}`

const qGroups = `query Groups {
	groups {
		id
		name
		policy
		policyParams { key val }
		nodes { id name address protocol tag subscriptionID }
	}
}`

// qGroupsRich additionally fetches each group's attached subscriptions and
// the nodes they contribute (the daed web UI fetches the same shape). On
// daed builds older than the GroupSubscription type this query fails schema
// validation and the driver falls back to qGroups.
const qGroupsRich = `query Groups {
	groups {
		id
		name
		policy
		policyParams { key val }
		nodes { id name address protocol tag subscriptionID }
		subscriptions {
			nameFilterRegex
			matchedCount
			subscription { id tag }
			matchedNodes { id name address protocol tag subscriptionID }
		}
	}
}`

const qAllNodes = `query AllNodes($first: Int, $after: ID) {
	nodes(first: $first, after: $after) {
		edges { id name address protocol tag subscriptionID }
		pageInfo { endCursor hasNextPage }
	}
}`

const mGroupAddSubscriptions = `mutation GroupAddSubscriptions($id: ID!, $subscriptionIDs: [ID!]!, $nameFilterRegex: String) {
	groupAddSubscriptions(id: $id, subscriptionIDs: $subscriptionIDs, nameFilterRegex: $nameFilterRegex)
}`

const mGroupDelSubscriptions = `mutation GroupDelSubscriptions($id: ID!, $subscriptionIDs: [ID!]!) {
	groupDelSubscriptions(id: $id, subscriptionIDs: $subscriptionIDs)
}`

const mGroupAddNodes = `mutation GroupAddNodes($id: ID!, $nodeIDs: [ID!]!) {
	groupAddNodes(id: $id, nodeIDs: $nodeIDs)
}`

const mGroupDelNodes = `mutation GroupDelNodes($id: ID!, $nodeIDs: [ID!]!) {
	groupDelNodes(id: $id, nodeIDs: $nodeIDs)
}`

const mCreateGroup = `mutation CreateGroup($name: String!, $policy: Policy!, $policyParams: [PolicyParam!]) {
	createGroup(name: $name, policy: $policy, policyParams: $policyParams) { id }
}`

const mRemoveGroup = `mutation RemoveGroup($id: ID!) { removeGroup(id: $id) }`

const mRenameGroup = `mutation RenameGroup($id: ID!, $name: String!) { renameGroup(id: $id, name: $name) }`

const mImportNodes = `mutation ImportNodes($rollbackError: Boolean!, $args: [ImportArgument!]!) {
	importNodes(rollbackError: $rollbackError, args: $args) { link }
}`

const mRemoveNodes = `mutation RemoveNodes($ids: [ID!]!) { removeNodes(ids: $ids) }`

const mGroupSetPolicy = `mutation GroupSetPolicy($id: ID!, $policy: Policy!, $policyParams: [PolicyParam!]) {
	groupSetPolicy(id: $id, policy: $policy, policyParams: $policyParams)
}`

const mTestNodeLatencies = `mutation TestNodeLatencies($ids: [ID!]) {
	testNodeLatencies(ids: $ids) { id }
}`

const qNodeLatencies = `query NodeLatencies($ids: [ID!]) {
	nodeLatencies(ids: $ids) { id latencyMs alive testedAt message }
}`

const qNodesBySubscription = `query NodesBySubscription($subscriptionId: ID!, $first: Int, $after: ID) {
	nodes(subscriptionId: $subscriptionId, first: $first, after: $after) {
		totalCount
		edges { id name address protocol tag subscriptionID }
		pageInfo { endCursor hasNextPage }
	}
}`

const qRuntimeOverview = `query RuntimeOverview($windowSec: Int!, $maxPoints: Int!) {
	general {
		runtimeOverview(windowSec: $windowSec, maxPoints: $maxPoints) {
			updatedAt
			uploadRate
			downloadRate
			uploadTotal
			downloadTotal
			activeConnections
			udpSessions
			samples { timestamp uploadRate downloadRate }
		}
	}
}`

const qSubscriptions = `query Subscriptions {
	subscriptions {
		id
		tag
		link
		status
		info
		updatedAt
		cronEnable
		nodes { totalCount }
	}
}`

const mImportSubscription = `mutation ImportSubscription($rollbackError: Boolean!, $arg: ImportArgument!) {
	importSubscription(rollbackError: $rollbackError, arg: $arg) { link }
}`

const mUpdateSubscription = `mutation UpdateSubscription($id: ID!) {
	updateSubscription(id: $id) { id }
}`

const mRemoveSubscriptions = `mutation RemoveSubscriptions($ids: [ID!]!) {
	removeSubscriptions(ids: $ids)
}`

const mUpdateSubscriptionCron = `mutation UpdateSubscriptionCron($id: ID!, $cronExp: String!, $cronEnable: Boolean!) {
	updateSubscriptionCron(id: $id, cronExp: $cronExp, cronEnable: $cronEnable) { id }
}`

// One document for all three sections: they are all root query fields.
const qSelections = `query Selections {
	configs {
		id
		name
		selected
		global {
			logLevel lanInterface wanInterface tcpCheckUrl udpCheckDns
			checkInterval checkTolerance dialMode allowInsecure
			tcpCheckHttpMethod sniffingTimeout mptcp pprofPort
			autoConfigKernelParameter autoConfigFirewallRule
		}
	}
	dnss { id name selected dns { string } }
	routings { id name selected routing { string } }
}`

const mCreateConfig = `mutation CreateConfig($name: String) { createConfig(name: $name) { id } }`

const mCreateDns = `mutation CreateDns($name: String, $dns: String) { createDns(name: $name, dns: $dns) { id } }`

const mCreateRouting = `mutation CreateRouting($name: String, $routing: String) { createRouting(name: $name, routing: $routing) { id } }`

const mRenameConfig = `mutation RenameConfig($id: ID!, $name: String!) { renameConfig(id: $id, name: $name) }`

const mRenameDns = `mutation RenameDns($id: ID!, $name: String!) { renameDns(id: $id, name: $name) }`

const mRenameRouting = `mutation RenameRouting($id: ID!, $name: String!) { renameRouting(id: $id, name: $name) }`

const mRemoveConfig = `mutation RemoveConfig($id: ID!) { removeConfig(id: $id) }`

const mRemoveDns = `mutation RemoveDns($id: ID!) { removeDns(id: $id) }`

const mRemoveRouting = `mutation RemoveRouting($id: ID!) { removeRouting(id: $id) }`

const mUpdateDnsText = `mutation UpdateDns($id: ID!, $dns: String!) { updateDns(id: $id, dns: $dns) { id } }`

const mUpdateRoutingText = `mutation UpdateRouting($id: ID!, $routing: String!) { updateRouting(id: $id, routing: $routing) { id } }`

const mUpdateConfigGlobal = `mutation UpdateConfig($id: ID!, $global: globalInput!) { updateConfig(id: $id, global: $global) { id } }`

const mSelectConfig = `mutation SelectConfig($id: ID!) { selectConfig(id: $id) }`

const mSelectDns = `mutation SelectDns($id: ID!) { selectDns(id: $id) }`

const mSelectRouting = `mutation SelectRouting($id: ID!) { selectRouting(id: $id) }`

const mRun = `mutation Run($dry: Boolean!) { run(dry: $dry) }`
