<template>
    <Views :loading="loading" :error="error">
        <div v-if="stability">
            <div class="north-star mb-6">
                <div class="north-star-title text-h6 mb-3">
                    <v-icon class="mr-1">mdi-compass-outline</v-icon>
                    North Star Metrics
                </div>
                <div class="cards-row">
                    <div class="metric-card" :class="statusClass(stability.north_star.availability_status)">
                        <div class="metric-label">System Availability</div>
                        <div class="metric-value">{{ stability.north_star.availability }}</div>
                        <div class="metric-sub">
                            {{ formatNumber(stability.north_star.total_requests) }} requests
                        </div>
                    </div>
                    <div class="metric-card" :class="statusClass(stability.north_star.latency_status)">
                        <div class="metric-label">System Latency (weighted P99)</div>
                        <div class="metric-value">{{ stability.north_star.latency }}</div>
                        <div class="metric-sub">traffic-weighted across services</div>
                    </div>
                    <div class="metric-card">
                        <div class="metric-label">Service Health</div>
                        <div class="metric-value">{{ stability.north_star.healthy_count }}/{{ stability.north_star.service_count }}</div>
                        <div class="metric-sub">services meeting SLO</div>
                    </div>
                </div>
            </div>

            <div class="compliance mb-6">
                <div class="section-title text-h6 mb-3">
                    <v-icon class="mr-1">mdi-check-decagram-outline</v-icon>
                    SLO Compliance
                </div>
                <div class="cards-row">
                    <div class="metric-card compliance-rate">
                        <div class="metric-label">Compliance Rate</div>
                        <div class="metric-value" :class="complianceColor">{{ stability.compliance.rate }}</div>
                        <div class="metric-sub">{{ stability.compliance.compliant }} of {{ stability.compliance.total }} services</div>
                    </div>
                    <div class="compliance-bars">
                        <div class="bar-group">
                            <div class="bar-label">
                                <span class="dot green" /> Compliant
                            </div>
                            <div class="bar-value">{{ stability.compliance.compliant }}</div>
                        </div>
                        <div class="bar-group">
                            <div class="bar-label">
                                <span class="dot orange" /> Warning
                            </div>
                            <div class="bar-value">{{ stability.compliance.warning }}</div>
                        </div>
                        <div class="bar-group">
                            <div class="bar-label">
                                <span class="dot red" /> Critical
                            </div>
                            <div class="bar-value">{{ stability.compliance.critical }}</div>
                        </div>
                    </div>
                </div>
            </div>

            <div class="leading mb-6">
                <div class="section-title text-h6 mb-3">
                    <v-icon class="mr-1">mdi-trending-up</v-icon>
                    Leading Indicators
                </div>
                <div class="cards-row">
                    <div class="metric-card">
                        <div class="metric-label">Deployments</div>
                        <div class="metric-value">{{ stability.leading.deployment_count }}</div>
                        <div class="metric-sub">in current window</div>
                    </div>
                    <div class="metric-card" :class="stability.leading.open_incidents > 0 ? 'status-critical' : ''">
                        <div class="metric-label">Open Incidents</div>
                        <div class="metric-value">{{ stability.leading.open_incidents }}</div>
                        <div class="metric-sub">{{ stability.leading.incident_count }} total</div>
                    </div>
                    <div class="metric-card">
                        <div class="metric-label">Avg MTTR</div>
                        <div class="metric-value">{{ stability.leading.avg_mttr }}</div>
                        <div class="metric-sub">mean time to recover</div>
                    </div>
                    <div class="metric-card" :class="stability.leading.slo_violations > 0 ? 'status-warning' : ''">
                        <div class="metric-label">SLO Violations</div>
                        <div class="metric-value">{{ stability.leading.slo_violations }}</div>
                        <div class="metric-sub">services currently violating</div>
                    </div>
                </div>
            </div>

            <div class="services-detail">
                <div class="section-title text-h6 mb-3">
                    <v-icon class="mr-1">mdi-format-list-bulleted</v-icon>
                    Per-Service Stability
                </div>
                <v-data-table
                    dense
                    class="table"
                    mobile-breakpoint="0"
                    :items-per-page="50"
                    :items="stability.services || []"
                    no-data-text="No services with SLI data"
                    :headers="headers"
                    :footer-props="{ itemsPerPageOptions: [10, 20, 50, 100, -1] }"
                >
                    <template #item.id="{ item }">
                        <router-link
                            :to="{ name: 'overview', params: { view: 'applications', id: item.id }, query: $utils.contextQuery() }"
                            class="service-name"
                        >
                            {{ $utils.appId(item.id).name }}
                        </router-link>
                    </template>
                    <template #item.availability="{ item }">
                        <span class="value" :class="item.availability_status">{{ item.availability || '–' }}</span>
                    </template>
                    <template #item.latency="{ item }">
                        <span class="value" :class="item.latency_status">{{ item.latency || '–' }}</span>
                    </template>
                    <template #item.slo_status="{ item }">
                        <span class="slo-badge" :class="item.slo_status">
                            {{ sloLabel(item.slo_status) }}
                        </span>
                    </template>
                    <template #item.error_budget="{ item }">
                        <span :class="budgetClass(item.error_budget)">{{ item.error_budget || '–' }}</span>
                    </template>
                    <template #item.has_incident="{ item }">
                        <v-icon v-if="item.has_incident" small color="red">mdi-alert-circle</v-icon>
                        <span v-else class="grey--text">–</span>
                    </template>
                    <template #item.deployments="{ item }">
                        {{ item.deployments || 0 }}
                    </template>
                </v-data-table>
            </div>
        </div>
        <div v-else-if="!loading && !error" class="text-center grey--text mt-10">
            No stability data available. Ensure services have SLO configurations.
        </div>
    </Views>
</template>

<script>
import Views from '@/views/Views.vue';

export default {
    components: { Views },

    data() {
        return {
            stability: null,
            loading: false,
            error: '',
        };
    },

    mounted() {
        this.get();
        this.$events.watch(this, this.get, 'refresh');
    },

    computed: {
        headers() {
            return [
                { value: 'id', text: 'Service', sortable: false },
                { value: 'availability', text: 'Availability', sortable: false, align: 'end' },
                { value: 'latency', text: 'Latency (P99)', sortable: false, align: 'end' },
                { value: 'slo_status', text: 'SLO Status', sortable: false, align: 'center' },
                { value: 'error_budget', text: 'Error Budget', sortable: false, align: 'end' },
                { value: 'has_incident', text: 'Incident', sortable: false, align: 'center' },
                { value: 'deployments', text: 'Deploys', sortable: false, align: 'end' },
            ];
        },
        complianceColor() {
            if (!this.stability || !this.stability.compliance) return '';
            const rate = parseFloat(this.stability.compliance.rate);
            if (isNaN(rate)) return '';
            if (rate >= 90) return 'green--text';
            if (rate >= 70) return 'orange--text';
            return 'red--text';
        },
    },

    methods: {
        get() {
            this.loading = true;
            this.$api.getOverview('stability', '', (data, error) => {
                this.loading = false;
                if (error) {
                    this.error = error;
                    return;
                }
                this.stability = data && data.stability ? data.stability : null;
            });
        },
        statusClass(status) {
            if (status === 'critical') return 'status-critical';
            if (status === 'warning') return 'status-warning';
            if (status === 'ok') return 'status-ok';
            return '';
        },
        sloLabel(status) {
            if (status === 'critical') return 'Violated';
            if (status === 'warning') return 'At Risk';
            if (status === 'ok') return 'Compliant';
            return 'N/A';
        },
        budgetClass(budget) {
            if (!budget || budget === '–') return 'grey--text';
            const val = parseFloat(budget);
            if (isNaN(val)) return '';
            if (val <= 0) return 'red--text font-weight-bold';
            if (val < 30) return 'orange--text';
            return 'green--text';
        },
        formatNumber(n) {
            if (n == null || n === 0) return '0';
            if (n >= 1e9) return (n / 1e9).toFixed(1) + 'B';
            if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M';
            if (n >= 1e3) return (n / 1e3).toFixed(1) + 'K';
            return Math.round(n).toString();
        },
    },
};
</script>

<style scoped>
.cards-row {
    display: flex;
    flex-wrap: wrap;
    gap: 16px;
}
.metric-card {
    flex: 1;
    min-width: 180px;
    border: 1px solid rgba(128, 128, 128, 0.2);
    border-radius: 8px;
    padding: 20px;
    text-align: center;
    transition: border-color 0.2s;
}
.metric-card.status-ok {
    border-color: #23d160;
    background: rgba(35, 209, 96, 0.04);
}
.metric-card.status-warning {
    border-color: #ffdd57;
    background: rgba(255, 221, 87, 0.06);
}
.metric-card.status-critical {
    border-color: #f44034;
    background: rgba(244, 64, 52, 0.04);
}
.metric-label {
    font-size: 13px;
    color: grey;
    margin-bottom: 8px;
    text-transform: uppercase;
    letter-spacing: 0.5px;
}
.metric-value {
    font-size: 28px;
    font-weight: 600;
    line-height: 1.2;
}
.metric-sub {
    font-size: 12px;
    color: grey;
    margin-top: 6px;
}

.compliance-bars {
    flex: 1;
    min-width: 180px;
    display: flex;
    flex-direction: column;
    gap: 10px;
    justify-content: center;
    padding: 12px 20px;
    border: 1px solid rgba(128, 128, 128, 0.2);
    border-radius: 8px;
}
.bar-group {
    display: flex;
    align-items: center;
    justify-content: space-between;
}
.bar-label {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 14px;
}
.bar-value {
    font-weight: 600;
    font-size: 16px;
}
.dot {
    width: 10px;
    height: 10px;
    border-radius: 50%;
    display: inline-block;
}
.dot.green {
    background: #23d160;
}
.dot.orange {
    background: #ffdd57;
}
.dot.red {
    background: #f44034;
}

.compliance-rate {
    max-width: 260px;
}

.table:deep(table) {
    min-width: 600px;
}
.table:deep(th) {
    white-space: nowrap;
}
.service-name {
    max-width: 30ch;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    display: inline-block;
}
.value.critical {
    color: #f44034;
    font-weight: 600;
}
.value.warning {
    color: #ff9800;
    font-weight: 600;
}
.value.ok {
    color: #23d160;
}
.slo-badge {
    padding: 2px 10px;
    border-radius: 12px;
    font-size: 12px;
    font-weight: 600;
    white-space: nowrap;
}
.slo-badge.ok {
    background: rgba(35, 209, 96, 0.12);
    color: #23d160;
}
.slo-badge.warning {
    background: rgba(255, 152, 0, 0.12);
    color: #ff9800;
}
.slo-badge.critical {
    background: rgba(244, 64, 52, 0.12);
    color: #f44034;
}
.slo-badge.unknown {
    background: rgba(128, 128, 128, 0.1);
    color: grey;
}

.section-title {
    display: flex;
    align-items: center;
}
.north-star-title {
    display: flex;
    align-items: center;
}
</style>
