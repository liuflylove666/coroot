<template>
    <div>
        <v-alert v-if="error" color="red" icon="mdi-alert-octagon-outline" outlined text class="mt-2">
            {{ error }}
        </v-alert>
        <v-alert v-if="sso_provider === 'saml'" color="info" outlined text>
            SAML configuration is visible for compatibility, but this local build currently supports OIDC login.
        </v-alert>
        <v-alert v-if="readonly" color="primary" outlined text>
            Single Sign-On is configured through the config and cannot be modified via the UI.
        </v-alert>
        <v-simple-table v-if="status !== 403" dense class="params">
            <tbody>
                <tr>
                    <td class="font-weight-medium text-no-wrap">Status</td>
                    <td>
                        <div v-if="enabled">
                            <v-icon color="success" class="mr-1" size="20">mdi-check-circle</v-icon>
                            Enabled
                        </div>
                        <div v-else>Disabled</div>
                    </td>
                </tr>
                <tr>
                    <td class="font-weight-medium text-no-wrap">Provider</td>
                    <td>
                        <v-radio-group v-model="sso_provider" :disabled="disabled || readonly" row hide-details dense class="mt-0">
                            <v-radio label="SAML 2.0" value="saml"></v-radio>
                            <v-radio label="OIDC" value="oidc"></v-radio>
                            <v-radio label="OpenLDAP" value="ldap"></v-radio>
                        </v-radio-group>
                    </td>
                </tr>

                <template v-if="sso_provider === 'saml'">
                    <tr>
                        <td class="font-weight-medium text-no-wrap">Identity Provider:</td>
                        <td>
                            <span v-if="provider" style="vertical-align: middle">{{ provider }}</span>
                            <input ref="file" type="file" accept=".xml" @change="upload" class="d-none" />
                            <v-btn v-if="!provider" color="primary" small :disabled="disabled || loading || readonly" @click="$refs.file.click()">
                                Upload Identity Provider Metadata XML
                            </v-btn>
                            <v-btn v-else :disabled="disabled || loading || readonly" small icon @click="$refs.file.click()">
                                <v-icon small>mdi-pencil</v-icon>
                            </v-btn>
                        </td>
                    </tr>
                    <tr>
                        <td class="font-weight-medium text-no-wrap">Service Provider Issuer / Identity ID:</td>
                        <td>{{ saml_asc_url }} <CopyButton :text="saml_asc_url" :disabled="disabled" /></td>
                    </tr>
                    <tr>
                        <td class="font-weight-medium text-no-wrap">Service Provider ACS URL / Single Sign On URL:</td>
                        <td>{{ saml_asc_url }} <CopyButton :text="saml_asc_url" :disabled="disabled" /></td>
                    </tr>
                    <tr>
                        <td class="font-weight-medium text-no-wrap">Attribute mapping:</td>
                        <td>
                            Coroot expects to receive the <b>Email</b>, <b>FirstName</b>, and <b>LastName</b> attributes.
                            <br />
                            Please configure Attribute Mapping on your Identity Provider's side.
                        </td>
                    </tr>
                </template>

                <template v-if="sso_provider === 'oidc'">
                    <tr>
                        <td class="font-weight-medium text-no-wrap">Issuer URL:</td>
                        <td>
                            <v-text-field
                                v-model="oidc.issuer_url"
                                :disabled="disabled || readonly"
                                outlined
                                dense
                                hide-details
                                placeholder="https://accounts.google.com"
                                class="oidc-input"
                            />
                        </td>
                    </tr>
                    <tr>
                        <td class="font-weight-medium text-no-wrap">Client ID:</td>
                        <td>
                            <v-text-field v-model="oidc.client_id" :disabled="disabled || readonly" outlined dense hide-details class="oidc-input" />
                        </td>
                    </tr>
                    <tr>
                        <td class="font-weight-medium text-no-wrap">Client Secret:</td>
                        <td>
                            <v-text-field
                                v-model="oidc.client_secret"
                                :disabled="disabled || readonly"
                                outlined
                                dense
                                hide-details
                                type="password"
                                :placeholder="oidc_has_secret ? '****************' : ''"
                                class="oidc-input"
                            />
                        </td>
                    </tr>
                    <tr>
                        <td class="font-weight-medium text-no-wrap">Redirect URI:</td>
                        <td>
                            {{ oidc_callback_url }} <CopyButton :text="oidc_callback_url" :disabled="disabled" />
                            <div class="caption grey--text mt-1">Configure this as the authorized redirect URL in your OIDC provider.</div>
                        </td>
                    </tr>
                    <tr>
                        <td class="font-weight-medium text-no-wrap">Claims:</td>
                        <td>Coroot expects to receive the <b>email</b>, <b>given_name</b>, and <b>family_name</b> claims from the ID token.</td>
                    </tr>
                </template>

                <template v-if="sso_provider === 'ldap'">
                    <tr>
                        <td class="font-weight-medium text-no-wrap">LDAP URL:</td>
                        <td><v-text-field v-model="ldap.url" :disabled="disabled || readonly" outlined dense hide-details placeholder="ldap://ldap.example.com:389" class="oidc-input" /></td>
                    </tr>
                    <tr>
                        <td class="font-weight-medium text-no-wrap">StartTLS:</td>
                        <td><v-checkbox v-model="ldap.start_tls" :disabled="disabled || readonly" hide-details dense class="mt-0" label="Upgrade ldap:// connection with StartTLS" /></td>
                    </tr>
                    <tr>
                        <td class="font-weight-medium text-no-wrap">TLS verification:</td>
                        <td><v-checkbox v-model="ldap.insecure_skip_verify" :disabled="disabled || readonly" hide-details dense class="mt-0" label="Skip certificate verification" /></td>
                    </tr>
                    <tr>
                        <td class="font-weight-medium text-no-wrap">Bind DN:</td>
                        <td><v-text-field v-model="ldap.bind_dn" :disabled="disabled || readonly" outlined dense hide-details placeholder="cn=readonly,dc=example,dc=com" class="oidc-input" /></td>
                    </tr>
                    <tr>
                        <td class="font-weight-medium text-no-wrap">Bind Password:</td>
                        <td>
                            <v-text-field
                                v-model="ldap.bind_password"
                                :disabled="disabled || readonly"
                                outlined
                                dense
                                hide-details
                                type="password"
                                :placeholder="ldap_has_bind_password ? '****************' : ''"
                                class="oidc-input"
                            />
                        </td>
                    </tr>
                    <tr>
                        <td class="font-weight-medium text-no-wrap">Base DN:</td>
                        <td><v-text-field v-model="ldap.base_dn" :disabled="disabled || readonly" outlined dense hide-details placeholder="ou=users,dc=example,dc=com" class="oidc-input" /></td>
                    </tr>
                    <tr>
                        <td class="font-weight-medium text-no-wrap">User filter:</td>
                        <td>
                            <v-text-field v-model="ldap.user_filter" :disabled="disabled || readonly" outlined dense hide-details placeholder="(uid={username})" class="oidc-input" />
                            <div class="caption grey--text mt-1">Use <code>{username}</code> or <code>{email}</code> as the login placeholder.</div>
                        </td>
                    </tr>
                    <tr>
                        <td class="font-weight-medium text-no-wrap">Email attribute:</td>
                        <td><v-text-field v-model="ldap.email_attribute" :disabled="disabled || readonly" outlined dense hide-details placeholder="mail" class="oidc-input" /></td>
                    </tr>
                    <tr>
                        <td class="font-weight-medium text-no-wrap">Name attribute:</td>
                        <td><v-text-field v-model="ldap.name_attribute" :disabled="disabled || readonly" outlined dense hide-details placeholder="cn" class="oidc-input" /></td>
                    </tr>
                </template>

                <tr v-if="enabled">
                    <td class="font-weight-medium text-no-wrap">Force SSO</td>
                    <td>
                        <v-checkbox
                            v-model="force_sso"
                            :disabled="disabled || readonly"
                            hide-details
                            dense
                            class="mt-0"
                            label="Disable password login and only allow SSO"
                        />
                    </td>
                </tr>
                <tr>
                    <td class="font-weight-medium text-no-wrap">Default role:</td>
                    <td>
                        <v-select
                            v-model="default_role"
                            :items="roles"
                            :disabled="disabled || readonly"
                            outlined
                            dense
                            :menu-props="{ offsetY: true }"
                            :rules="[$validators.notEmpty]"
                            hide-details
                            class="roles"
                        />
                    </td>
                </tr>
            </tbody>
        </v-simple-table>
        <div v-if="status !== 403" class="d-flex mt-2" style="gap: 8px">
            <v-btn color="primary" small :disabled="disabled || loading || readonly || !canSave" @click="save">
                Save <template v-if="!enabled">and Enable</template>
            </v-btn>
            <v-btn v-if="enabled" color="error" small :disabled="disabled || loading || readonly" @click="disable">Disable</v-btn>
        </div>
    </div>
</template>

<script>
import CopyButton from '@/components/CopyButton.vue';

export default {
    components: { CopyButton },
    computed: {
        saml_asc_url() {
            return location.origin + this.$coroot.base_path + 'sso/saml';
        },
        oidc_callback_url() {
            return location.origin + this.$coroot.base_path + 'sso/oidc';
        },
        canSave() {
            if (this.sso_provider === 'saml') {
                return !!this.provider;
            } else if (this.sso_provider === 'oidc') {
                return !!(this.oidc.issuer_url && this.oidc.client_id && (this.oidc.client_secret || this.oidc_has_secret));
            } else if (this.sso_provider === 'ldap') {
                return !!(this.ldap.url && this.ldap.base_dn && this.ldap.user_filter && (this.ldap.bind_password || this.ldap_has_bind_password || !this.ldap.bind_dn));
            }
            return false;
        },
    },

    data() {
        return {
            disabled: false,
            readonly: false,
            loading: false,
            error: '',
            status: undefined,
            enabled: false,
            force_sso: false,
            sso_provider: 'oidc',
            default_role: '',
            provider: '',
            roles: [],
            oidc: {
                issuer_url: '',
                client_id: '',
                client_secret: '',
            },
            oidc_has_secret: false,
            ldap: {
                url: '',
                start_tls: false,
                insecure_skip_verify: false,
                bind_dn: '',
                bind_password: '',
                base_dn: '',
                user_filter: '(uid={username})',
                email_attribute: 'mail',
                name_attribute: 'cn',
            },
            ldap_has_bind_password: false,
        };
    },

    mounted() {
        this.$events.watch(this, this.get, 'roles');
        this.get();
    },

    methods: {
        get() {
            this.loading = true;
            this.error = '';
            this.status = undefined;
            this.$api.sso(null, (data, error, status) => {
                this.loading = false;
                if (error) {
                    this.error = error;
                    this.status = status;
                    return;
                }
                this.readonly = data.readonly;
                this.enabled = data.enabled;
                this.force_sso = data.force_sso || false;
                this.sso_provider = data.sso_provider || 'saml';
                this.default_role = data.default_role;
                this.provider = data.provider;
                this.roles = data.roles || [];

                if (data.oidc) {
                    this.oidc = {
                        issuer_url: data.oidc.issuer_url || '',
                        client_id: data.oidc.client_id || '',
                        client_secret: '', // Never returned from backend
                    };
                    this.oidc_has_secret = !!(data.oidc.issuer_url && data.oidc.client_id);
                } else {
                    this.oidc_has_secret = false;
                }
                if (data.ldap) {
                    this.ldap = {
                        url: data.ldap.url || '',
                        start_tls: !!data.ldap.start_tls,
                        insecure_skip_verify: !!data.ldap.insecure_skip_verify,
                        bind_dn: data.ldap.bind_dn || '',
                        bind_password: '',
                        base_dn: data.ldap.base_dn || '',
                        user_filter: data.ldap.user_filter || '(uid={username})',
                        email_attribute: data.ldap.email_attribute || 'mail',
                        name_attribute: data.ldap.name_attribute || 'cn',
                    };
                    this.ldap_has_bind_password = !!data.ldap.bind_dn;
                } else {
                    this.ldap_has_bind_password = false;
                }
            });
        },
        post(action, metadata) {
            this.loading = true;
            this.error = '';
            this.status = undefined;
            const form = {
                action,
                provider: this.sso_provider,
                default_role: this.default_role,
                force_sso: this.force_sso,
            };

            if (this.sso_provider === 'saml' && metadata) {
                form.saml = { metadata };
            } else if (this.sso_provider === 'oidc' && action === 'save') {
                form.oidc = {
                    issuer_url: this.oidc.issuer_url,
                    client_id: this.oidc.client_id,
                };
                if (this.oidc.client_secret) {
                    form.oidc.client_secret = this.oidc.client_secret;
                }
            } else if (this.sso_provider === 'ldap' && action === 'save') {
                form.ldap = {
                    url: this.ldap.url,
                    start_tls: this.ldap.start_tls,
                    insecure_skip_verify: this.ldap.insecure_skip_verify,
                    bind_dn: this.ldap.bind_dn,
                    base_dn: this.ldap.base_dn,
                    user_filter: this.ldap.user_filter,
                    email_attribute: this.ldap.email_attribute,
                    name_attribute: this.ldap.name_attribute,
                };
                if (this.ldap.bind_password) {
                    form.ldap.bind_password = this.ldap.bind_password;
                }
            }

            this.$api.sso(form, (data, error, status) => {
                this.loading = false;
                if (error) {
                    this.error = error;
                    this.status = status;
                    return;
                }
                this.get();
            });
        },
        save() {
            this.post('save');
        },
        disable() {
            this.post('disable');
        },
        upload(e) {
            const file = e.target.files[0];
            e.target.value = '';
            if (!file) {
                return;
            }
            file.text().then((v) => {
                this.post('upload', v);
            });
        },
    },
};
</script>

<style scoped>
.params:deep(td) {
    padding: 4px 16px !important;
}
.params:deep(td:first-child) {
    width: 280px;
}
.roles {
    max-width: 20ch;
}
.roles:deep(.v-input__slot) {
    min-height: initial !important;
    height: 2rem !important;
    padding: 0 8px !important;
}
.roles:deep(.v-input__append-inner) {
    margin-top: 4px !important;
}
.oidc-input {
    max-width: 500px;
}
.oidc-input:deep(.v-input__slot) {
    min-height: initial !important;
    height: 2rem !important;
    padding: 0 8px !important;
}
</style>
