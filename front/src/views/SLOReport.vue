<template>
    <Views :loading="loading" :error="error">
        <div class="d-flex align-center mb-4 flex-wrap" style="gap: 8px">
            <h1 class="text-h5 mb-0">SLO Report</h1>
            <v-spacer />
            <span class="caption grey--text mr-2">Window:</span>
            <v-btn
                v-for="p in presets"
                :key="p.id"
                x-small
                outlined
                :color="isPresetActive(p) ? 'primary' : ''"
                @click="applyPreset(p)"
            >
                {{ p.label }}
            </v-btn>
        </div>

        <div v-if="report">
            <div class="caption grey--text mb-4">
                {{ formatDate(report.window.from) }} → {{ formatDate(report.window.to) }}
                ({{ report.window.days.toFixed(1) }} days)
            </div>

            <div class="cards-row mb-6">
                <div class="metric-card" :class="complianceCardClass">
                    <div class="metric-label">Compliant Services</div>
                    <div class="metric-value">{{ report.summary.compliant }}/{{ report.summary.total_services }}</div>
                    <div class="metric-sub">
                        <span v-if="report.summary.violating" class="red--text">{{ report.summary.violating }} violating</span>
                        <span v-if="report.summary.violating && report.summary.at_risk"> · </span>
                        <span v-if="report.summary.at_risk" class="orange--text">{{ report.summary.at_risk }} at risk</span>
                        <span v-if="!report.summary.violating && !report.summary.at_risk">all healthy</span>
                    </div>
                </div>

                <div class="metric-card" :class="availCardClass">
                    <div class="metric-label">Avg Availability</div>
                    <div class="metric-value">{{ report.summary.avg_availability }}</div>
                    <div class="metric-sub">across services with availability SLO</div>
                </div>

                <div class="metric-card" :class="violationCardClass">
                    <div class="metric-label">Total Violation</div>
                    <div class="metric-value">{{ formatMinutes(report.summary.total_violation_minutes) }}</div>
                    <div class="metric-sub">{{ report.summary.incident_count }} SLO incidents</div>
                </div>

                <div class="metric-card" :class="budgetCardClass">
                    <div class="metric-label">Error Budget Remaining</div>
                    <div class="metric-value">{{ report.summary.avg_error_budget_remaining }}</div>
                    <div class="metric-sub">average across services</div>
                </div>
            </div>

            <div class="section mb-6">
                <div class="section-title text-h6 mb-3">
                    <v-icon class="mr-1">mdi-chart-line</v-icon>
                    Daily Trend
                </div>
                <div v-if="report.daily_trend && report.daily_trend.length" class="trend-card">
                    <div class="trend-row">
                        <div class="trend-label">System availability</div>
                        <svg :viewBox="`0 0 ${trendWidth} 60`" class="trend-svg" preserveAspectRatio="none">
                            <polyline
                                v-if="availabilityPoints"
                                :points="availabilityPoints"
                                fill="none"
                                stroke="#23d160"
                                stroke-width="1.5"
                                vector-effect="non-scaling-stroke"
                            />
                            <line x1="0" :x2="trendWidth" :y1="targetY" :y2="targetY" stroke="grey" stroke-dasharray="2 3" stroke-width="0.5" />
                        </svg>
                        <div class="trend-current">
                            {{ formatPct(latestAvailability) }}
                        </div>
                    </div>
                    <div class="trend-row">
                        <div class="trend-label">Services compliant</div>
                        <div class="trend-bars">
                            <div
                                v-for="(b, i) in report.daily_trend"
                                :key="i"
                                class="trend-bar"
                                :style="{ height: barHeight(b) + '%' }"
                                :title="`${formatDate(b.ts)}: ${b.services_compliant}/${b.services_total}`"
                            />
                        </div>
                        <div class="trend-current">
                            {{ latestCompliant }}
                        </div>
                    </div>
                    <div class="trend-axis">
                        <span>{{ formatDateShort(report.daily_trend[0].ts) }}</span>
                        <span>{{ formatDateShort(report.daily_trend[report.daily_trend.length - 1].ts) }}</span>
                    </div>
                </div>
                <div v-else class="grey--text">No data in this window.</div>
            </div>

            <div v-if="report.top_violators && report.top_violators.length" class="section mb-6">
                <div class="section-title text-h6 mb-3">
                    <v-icon class="mr-1">mdi-alert-octagon-outline</v-icon>
                    Top Violators
                </div>
                <div class="top-list">
                    <div v-for="s in report.top_violators" :key="s.id" class="top-row">
                        <router-link
                            :to="{ name: 'overview', params: { view: 'applications', id: s.id }, query: $utils.contextQuery() }"
                            class="top-name"
                        >
                            {{ $utils.appId(s.id).name }}
                        </router-link>
                        <div class="top-bar-wrap">
                            <div
                                class="top-bar"
                                :style="{ width: violatorBarWidth(s) + '%' }"
                                :class="s.slo_status === 'critical' ? 'critical' : 'warning'"
                            />
                        </div>
                        <div class="top-value">{{ formatMinutes(s.violation_minutes) }}</div>
                    </div>
                </div>
            </div>

            <div class="section mb-6">
                <div class="section-title text-h6 mb-3">
                    <v-icon class="mr-1">mdi-format-list-bulleted</v-icon>
                    Per-Service SLO Performance
                </div>
                <v-data-table
                    dense
                    class="table"
                    mobile-breakpoint="0"
                    :items-per-page="50"
                    :items="report.services || []"
                    :headers="serviceHeaders"
                    no-data-text="No services with SLO configurations in this window."
                    :footer-props="{ itemsPerPageOptions: [25, 50, 100, -1] }"
                >
                    <template #item.id="{ item }">
                        <router-link
                            :to="{ name: 'overview', params: { view: 'applications', id: item.id }, query: $utils.contextQuery() }"
                            class="service-name"
                        >
                            {{ $utils.appId(item.id).name }}
                        </router-link>
                        <span v-if="item.category" class="caption grey--text ml-1">({{ item.category }})</span>
                    </template>
                    <template #item.availability="{ item }">
                        <span v-if="item.has_availability" :class="availabilityValueClass(item)">
                            {{ item.availability || '–' }}
                        </span>
                        <span v-else class="grey--text">–</span>
                        <div v-if="item.has_availability" class="caption grey--text">target {{ item.availability_target }}</div>
                    </template>
                    <template #item.latency="{ item }">
                        <span v-if="item.has_latency" :class="latencyValueClass(item)">
                            {{ item.latency_pass_rate || '–' }}
                        </span>
                        <span v-else class="grey--text">–</span>
                        <div v-if="item.has_latency" class="caption grey--text">
                            &lt;{{ item.latency_objective_bucket }} target {{ item.latency_target }}
                        </div>
                    </template>
                    <template #item.error_budget_remaining="{ item }">
                        <span v-if="item.has_availability" :class="budgetValueClass(item.error_budget_remaining_pct)">
                            {{ item.error_budget_remaining }}
                        </span>
                        <span v-else class="grey--text">–</span>
                    </template>
                    <template #item.time_to_exhaustion="{ item }">
                        <span v-if="item.has_availability" :class="exhaustionClass(item.time_to_exhaustion)">
                            {{ item.time_to_exhaustion || '–' }}
                        </span>
                        <span v-else class="grey--text">–</span>
                    </template>
                    <template #item.violation_minutes="{ item }">
                        <span v-if="item.violation_minutes > 0" class="red--text">{{ formatMinutes(item.violation_minutes) }}</span>
                        <span v-else class="grey--text">0</span>
                    </template>
                    <template #item.incident_count="{ item }">
                        <span :class="item.incident_count > 0 ? 'red--text' : 'grey--text'">{{ item.incident_count }}</span>
                    </template>
                    <template #item.slo_status="{ item }">
                        <span class="status-badge" :class="item.slo_status">{{ statusLabel(item.slo_status) }}</span>
                    </template>
                </v-data-table>
            </div>

            <div v-if="report.violations && report.violations.length" class="section">
                <div class="section-title text-h6 mb-3">
                    <v-icon class="mr-1">mdi-history</v-icon>
                    SLO Violations in Window
                    <span class="caption grey--text ml-2">({{ report.violations.length }} incidents)</span>
                </div>
                <v-data-table
                    dense
                    class="table"
                    mobile-breakpoint="0"
                    :items-per-page="20"
                    :items="report.violations"
                    :headers="violationHeaders"
                    :footer-props="{ itemsPerPageOptions: [10, 20, 50, -1] }"
                >
                    <template #item.opened_at="{ item }">
                        <span :title="formatDateLong(item.opened_at)">{{ formatDateShort(item.opened_at) }}</span>
                    </template>
                    <template #item.app_name="{ item }">
                        <router-link
                            :to="{
                                name: 'overview',
                                params: { view: 'incidents' },
                                query: { ...$utils.contextQuery(), incident: item.incident_key },
                            }"
                            class="service-name"
                        >
                            {{ item.app_name }}
                        </router-link>
                        <span v-if="item.category" class="caption grey--text ml-1">({{ item.category }})</span>
                    </template>
                    <template #item.type="{ item }">
                        <span class="type-badge" :class="item.type">{{ item.type }}</span>
                    </template>
                    <template #item.duration_minutes="{ item }">
                        {{ formatMinutes(item.duration_minutes) }}
                    </template>
                    <template #item.severity="{ item }">
                        <span class="status-badge" :class="item.severity">{{ item.severity }}</span>
                    </template>
                    <template #item.short_description="{ item }">
                        <span class="grey--text text--darken-1">{{ item.short_description }}</span>
                    </template>
                </v-data-table>
            </div>
        </div>
        <div v-else-if="!loading && !error" class="text-center grey--text mt-10">
            No SLO data in this window. Configure SLOs under Inspections to populate this report.
        </div>
    </Views>
</template>

<script>
import Views from '@/views/Views.vue';

const PRESETS = [
    { id: '7d', label: 'Last 7d', from: 'now-7d', to: 'now' },
    { id: '30d', label: 'Last 30d', from: 'now-30d', to: 'now' },
    { id: '90d', label: 'Last 90d', from: 'now-90d', to: 'now' },
    { id: 'mtd', label: 'MTD', mtd: true, to: 'now' },
];

function monthStartMs() {
    const d = new Date();
    return new Date(d.getFullYear(), d.getMonth(), 1, 0, 0, 0, 0).getTime();
}

export default {
    components: { Views },

    data() {
        return {
            report: null,
            loading: false,
            error: '',
            presets: PRESETS,
        };
    },

    mounted() {
        if (!this.$route.query.from) {
            this.applyPreset(PRESETS[1]);
            return;
        }
        this.get();
        this.$events.watch(this, this.get, 'refresh');
    },

    computed: {
        complianceCardClass() {
            if (!this.report || !this.report.summary.total_services) return '';
            if (this.report.summary.violating > 0) return 'status-critical';
            if (this.report.summary.at_risk > 0) return 'status-warning';
            return 'status-ok';
        },
        availCardClass() {
            const pct = this.report && this.report.summary.avg_availability_pct;
            if (!pct) return '';
            if (pct < 99.0) return 'status-critical';
            if (pct < 99.9) return 'status-warning';
            return 'status-ok';
        },
        violationCardClass() {
            if (!this.report) return '';
            return this.report.summary.total_violation_minutes > 0 ? 'status-critical' : 'status-ok';
        },
        budgetCardClass() {
            const txt = this.report && this.report.summary.avg_error_budget_remaining;
            if (!txt || txt === '-') return '';
            const v = parseFloat(txt);
            if (isNaN(v)) return '';
            if (v <= 0) return 'status-critical';
            if (v < 30) return 'status-warning';
            return 'status-ok';
        },

        serviceHeaders() {
            return [
                { value: 'id', text: 'Service', sortable: false },
                { value: 'availability', text: 'Availability', sortable: false, align: 'end' },
                { value: 'latency', text: 'Latency Pass', sortable: false, align: 'end' },
                { value: 'error_budget_remaining', text: 'Budget Left', sortable: false, align: 'end' },
                { value: 'time_to_exhaustion', text: 'Time-to-Exhaust', sortable: false, align: 'end' },
                { value: 'violation_minutes', text: 'Violation', sortable: false, align: 'end' },
                { value: 'incident_count', text: 'Incidents', sortable: false, align: 'end' },
                { value: 'slo_status', text: 'Status', sortable: false, align: 'center' },
            ];
        },
        violationHeaders() {
            return [
                { value: 'opened_at', text: 'Opened', sortable: false },
                { value: 'app_name', text: 'Service', sortable: false },
                { value: 'type', text: 'Type', sortable: false },
                { value: 'duration_minutes', text: 'Duration', sortable: false, align: 'end' },
                { value: 'severity', text: 'Severity', sortable: false, align: 'center' },
                { value: 'short_description', text: 'Summary', sortable: false },
            ];
        },

        trendWidth() {
            return Math.max(100, (this.report?.daily_trend?.length || 1) * 10);
        },
        availabilityPoints() {
            const t = this.report?.daily_trend;
            if (!t || !t.length) return null;
            const w = this.trendWidth;
            const step = w / Math.max(1, t.length - 1);
            const minPct = Math.min(...t.map((b) => (b.availability > 0 ? b.availability : 100)));
            const lo = Math.min(95, Math.floor(minPct));
            const hi = 100;
            return t
                .map((b, i) => {
                    const pct = b.availability > 0 ? b.availability : 100;
                    const x = i * step;
                    const y = 60 - ((pct - lo) / (hi - lo)) * 60;
                    return `${x.toFixed(1)},${y.toFixed(1)}`;
                })
                .join(' ');
        },
        targetY() {
            const t = this.report?.daily_trend;
            if (!t || !t.length) return 0;
            const minPct = Math.min(...t.map((b) => (b.availability > 0 ? b.availability : 100)));
            const lo = Math.min(95, Math.floor(minPct));
            return 60 - ((99.9 - lo) / (100 - lo)) * 60;
        },
        latestAvailability() {
            const t = this.report?.daily_trend;
            if (!t || !t.length) return 0;
            for (let i = t.length - 1; i >= 0; i--) {
                if (t[i].availability > 0) return t[i].availability;
            }
            return 0;
        },
        latestCompliant() {
            const t = this.report?.daily_trend;
            if (!t || !t.length) return '';
            const last = t[t.length - 1];
            return `${last.services_compliant}/${last.services_total}`;
        },
    },

    methods: {
        get() {
            this.loading = true;
            this.error = '';
            this.$api.getOverview('slo', '', (data, error) => {
                this.loading = false;
                if (error) {
                    this.error = error;
                    return;
                }
                this.report = data && data.slo_report ? data.slo_report : null;
            });
        },
        applyPreset(p) {
            const { from, to, incident, alert, ...query } = this.$route.query;
            void from;
            void to;
            void incident;
            void alert;
            const fromVal = p.mtd ? String(monthStartMs()) : p.from;
            this.$router.push({ query: { ...query, from: fromVal, to: p.to } }).catch((err) => err);
        },
        isPresetActive(p) {
            const cur = this.$route.query.from;
            const curTo = this.$route.query.to || 'now';
            if (p.mtd) {
                return cur === String(monthStartMs()) && curTo === p.to;
            }
            return cur === p.from && curTo === p.to;
        },
        formatDate(ts) {
            if (!ts) return '';
            return this.$format.date(ts, '{YYYY}-{MM}-{DD} {HH}:{mm}');
        },
        formatDateShort(ts) {
            if (!ts) return '';
            return this.$format.date(ts, '{MM}-{DD}');
        },
        formatDateLong(ts) {
            if (!ts) return '';
            return this.$format.date(ts, '{YYYY}-{MM}-{DD} {HH}:{mm}:{ss}');
        },
        formatMinutes(min) {
            if (!min || min <= 0) return '0m';
            if (min < 60) return `${min}m`;
            const h = Math.floor(min / 60);
            const m = min % 60;
            if (h < 24) return m ? `${h}h ${m}m` : `${h}h`;
            const d = Math.floor(h / 24);
            const rh = h % 24;
            return rh ? `${d}d ${rh}h` : `${d}d`;
        },
        formatPct(v) {
            if (!v) return '–';
            if (v >= 99.99) return v.toFixed(3) + '%';
            if (v >= 99) return v.toFixed(2) + '%';
            return v.toFixed(1) + '%';
        },
        availabilityValueClass(item) {
            const pct = item.availability_pct;
            const target = parseFloat(item.availability_target);
            if (!pct || isNaN(target)) return '';
            if (pct < target) return 'red--text font-weight-medium';
            return 'green--text';
        },
        latencyValueClass(item) {
            const pct = item.latency_pass_rate_pct;
            const target = parseFloat(item.latency_target);
            if (!pct || isNaN(target)) return '';
            if (pct < target) return 'red--text font-weight-medium';
            return 'green--text';
        },
        budgetValueClass(pct) {
            if (pct == null) return '';
            if (pct <= 0) return 'red--text font-weight-bold';
            if (pct < 30) return 'orange--text';
            return 'green--text';
        },
        exhaustionClass(text) {
            if (text === 'exhausted') return 'red--text font-weight-bold';
            if (text === '∞' || text === '>1y') return 'green--text';
            return '';
        },
        statusLabel(s) {
            if (s === 'critical') return 'Violated';
            if (s === 'warning') return 'At Risk';
            if (s === 'ok') return 'Compliant';
            return 'N/A';
        },
        violatorBarWidth(s) {
            if (!this.report.top_violators.length) return 0;
            const max = this.report.top_violators[0].violation_minutes || 1;
            return Math.max(2, (s.violation_minutes / max) * 100);
        },
        barHeight(b) {
            if (!b.services_total) return 4;
            return (b.services_compliant / b.services_total) * 100;
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
    padding: 18px;
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
    font-size: 12px;
    color: grey;
    margin-bottom: 6px;
    text-transform: uppercase;
    letter-spacing: 0.5px;
}
.metric-value {
    font-size: 26px;
    font-weight: 600;
    line-height: 1.2;
}
.metric-sub {
    font-size: 12px;
    color: grey;
    margin-top: 4px;
}
.section-title {
    display: flex;
    align-items: center;
}

.trend-card {
    border: 1px solid rgba(128, 128, 128, 0.2);
    border-radius: 8px;
    padding: 16px;
}
.trend-row {
    display: flex;
    align-items: center;
    gap: 12px;
    margin-bottom: 8px;
}
.trend-label {
    width: 160px;
    font-size: 12px;
    color: grey;
    flex-shrink: 0;
}
.trend-svg {
    flex: 1;
    height: 60px;
    width: 100%;
}
.trend-bars {
    flex: 1;
    height: 40px;
    display: flex;
    align-items: flex-end;
    gap: 1px;
}
.trend-bar {
    flex: 1;
    background: #23d160;
    min-height: 2px;
    border-radius: 1px 1px 0 0;
    opacity: 0.7;
}
.trend-current {
    width: 80px;
    text-align: right;
    font-weight: 600;
    font-size: 14px;
}
.trend-axis {
    display: flex;
    justify-content: space-between;
    font-size: 11px;
    color: grey;
    margin-left: 172px;
    margin-top: 4px;
}

.top-list {
    border: 1px solid rgba(128, 128, 128, 0.15);
    border-radius: 8px;
    padding: 8px 16px;
}
.top-row {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 6px 0;
    border-bottom: 1px solid rgba(128, 128, 128, 0.08);
}
.top-row:last-child {
    border-bottom: none;
}
.top-name {
    width: 200px;
    flex-shrink: 0;
    font-size: 13px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
.top-bar-wrap {
    flex: 1;
    height: 14px;
    background: rgba(128, 128, 128, 0.08);
    border-radius: 7px;
    overflow: hidden;
}
.top-bar {
    height: 100%;
    border-radius: 7px;
    transition: width 0.3s;
}
.top-bar.critical {
    background: #f44034;
}
.top-bar.warning {
    background: #ff9800;
}
.top-value {
    width: 80px;
    text-align: right;
    font-size: 13px;
    font-weight: 500;
}

.table:deep(table) {
    min-width: 700px;
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

.status-badge {
    padding: 2px 10px;
    border-radius: 12px;
    font-size: 12px;
    font-weight: 600;
    white-space: nowrap;
}
.status-badge.ok {
    background: rgba(35, 209, 96, 0.12);
    color: #23d160;
}
.status-badge.warning {
    background: rgba(255, 152, 0, 0.12);
    color: #ff9800;
}
.status-badge.critical {
    background: rgba(244, 64, 52, 0.12);
    color: #f44034;
}
.status-badge.unknown {
    background: rgba(128, 128, 128, 0.1);
    color: grey;
}

.type-badge {
    padding: 1px 8px;
    border-radius: 8px;
    font-size: 11px;
    text-transform: capitalize;
    background: rgba(128, 128, 128, 0.12);
}
.type-badge.availability {
    color: #f44034;
    background: rgba(244, 64, 52, 0.1);
}
.type-badge.latency {
    color: #ff9800;
    background: rgba(255, 152, 0, 0.1);
}
.type-badge.mixed {
    color: #9c27b0;
    background: rgba(156, 39, 176, 0.1);
}
</style>
