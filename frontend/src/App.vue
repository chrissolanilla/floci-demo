
<script setup>
import { ref, onMounted } from 'vue';

const user = ref(null);
const mode = ref('signup');
const username = ref('');
const password = ref('');
const file = ref(null);
const fileInput = ref(null);

const imageURL = ref('/api/image');
const loading = ref(true);
const busy = ref(false);
const error = ref('');
const message = ref('');

async function request(path, options = {}) {
	const response = await fetch(path, options);

	if (response.status === 204) {
		return null;
	}

	const data = await response.json();

	if (!response.ok) {
		if (response.status === 401) {
			user.value = null;
		}

		throw new Error(data.error || 'Request failed.');
	}

	return data;
}

async function loadUser() {
	try {
		const response = await fetch('/api/me');

		if (response.status === 401) {
			return;
		}

		if (!response.ok) {
			throw new Error('Could not load your session.');
		}

		user.value = await response.json();
	} catch (err) {
		error.value = err.message;
	} finally {
		loading.value = false;
	}
}

function switchMode() {
	if (mode.value === 'signup') {
		mode.value = 'login';
	} else {
		mode.value = 'signup';
	}

	password.value = '';
	error.value = '';
	message.value = '';
}

async function authenticate() {
	busy.value = true;
	error.value = '';
	message.value = '';

	try {
		user.value = await request('/api/' + mode.value, {
			method: 'POST',
			headers: {
				'Content-Type': 'application/json',
			},
			body: JSON.stringify({
				username: username.value,
				password: password.value,
			}),
		});

		password.value = '';
		imageURL.value = '/api/image?t=' + Date.now();
	} catch (err) {
		error.value = err.message;
	} finally {
		busy.value = false;
	}
}

function selectImage(event) {
	file.value = event.target.files[0] || null;
	error.value = '';
	message.value = '';

	if (!file.value) {
		return;
	}

	if (file.value.size > 5 * 1024 * 1024) {
		error.value = 'Choose an image smaller than 5 MiB.';
		file.value = null;
		event.target.value = '';
	}
}

async function upload() {
	if (!file.value) {
		return;
	}

	busy.value = true;
	error.value = '';
	message.value = '';

	try {
		const body = new FormData();
		body.append('image', file.value);

		user.value = await request('/api/image', {
			method: 'POST',
			body: body,
		});

		imageURL.value = '/api/image?t=' + Date.now();
		file.value = null;
		fileInput.value.value = '';
		message.value = 'Your image is saved in S3';
	} catch (err) {
		error.value = err.message;
	} finally {
		busy.value = false;
	}
}

async function logout() {
	busy.value = true;
	error.value = '';
	message.value = '';

	try {
		await request('/api/logout', {
			method: 'POST',
		});

		user.value = null;
		file.value = null;
		password.value = '';
	} catch (err) {
		error.value = err.message;
	} finally {
		busy.value = false;
	}
}

onMounted(loadUser);
</script>

<template>
	<main>
		<header>
			<h1>One Image Storage</h1>
			<p>You only get one image to store here</p>
		</header>

		<p v-if="loading">Loading your session...</p>

		<section v-else-if="!user" class="card">
			<h2 v-if="mode === 'signup'">Make yourself at home.</h2>
			<h2 v-else>Welcome back.</h2>

			<p v-if="mode === 'signup'">
				Create an account, then save your favorite image
			</p>
			<p v-else>
				Sign in to see your image.
			</p>

			<form @submit.prevent="authenticate">
				<label for="username">Username</label>
				<input
					id="username"
					v-model="username"
					autocomplete="username"
					required
					minlength="3"
					maxlength="24"
					pattern="[a-zA-Z0-9_]{3,24}"
					placeholder="your_name"
				>

				<label for="password">Password</label>
				<input
					id="password"
					v-model="password"
					type="password"
					required
					autocomplete="current-password"
				>

				<small>
					Username: 3-24 characters, password: 8-72 bytes(whatever u want bro)
				</small>

				<button class="text-button" v-if="mode === 'signup'" :disabled="busy">
					Create account
				</button>
				<button v-else :disabled="busy">
					Sign in
				</button>

				<button class="text-button" :disabled="busy" @click="switchMode">
					<span v-if="mode === 'signup'">
						Already have an account? Sign in
					</span>
					<span v-else>
						New here? Create an account
					</span>
				</button>
			</form>

		</section>

		<section v-else class="card">
			<div class="row">
				<h2>Hello, {{ user.username }}</h2>
				<button class="text-button" :disabled="busy" @click="logout">
					Sign out
				</button>
			</div>

			<img
				v-if="user.hasImage"
				:src="imageURL"
				alt="Your saved image"
				class="saved-image"
			>
			<div v-else class="empty-image">
				<p>upload an image</p>
			</div>

			<form @submit.prevent="upload">
				<label for="image">Choose an image</label>
				<input
					id="image"
					ref="fileInput"
					type="file"
					accept="image/png,image/jpeg,image/gif"
					:disabled="busy"
					@change="selectImage"
				>

				<small>
					PNG, JPEG, or GIF. Up to 5 MiB
				</small>

				<button :disabled="busy || !file">
					<span v-if="busy">Uploading...</span>
					<span v-else-if="user.hasImage">Replace image</span>
					<span v-else>Save image</span>
				</button>
			</form>
		</section>

		<p v-if="error" class="error" role="alert">{{ error }}</p>
		<p v-if="message" class="success" role="status">{{ message }}</p>
	</main>
</template>

<style scoped>
main {
	--special_var: calc(((268 / 300) * 100) * 1vw);
	display: flex;
	padding: 0.25rem;
	margin: 0.25rem;
	flex-direction: column;
	justify-content: center;
	text-align: center;
}
label{
	text-align:left;
}
form {
	display: flex;
	flex-direction: column;
}


.text-button {
	margin: 0.25rem;
	width: clamp(1rem, var(--special_var), 30rem);
	height: auto;
	margin-left: auto;
	margin-right: auto;
}
</style>
